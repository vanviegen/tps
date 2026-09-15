package daemon

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Save points: the state of a task at the end of an agent run, kept so that
// the task can be put back to it or a second task started from it. All three
// things that make up that state are already append-only, so a point is a
// position in each and costs nothing to keep:
//
//   - the work, as a commit in the task's own clone (a merge squashes those
//     back into one, see replant);
//   - the chat log, as the bytes up to the entry the point is;
//   - what claude remembers, as the length of each of its session
//     transcripts. It only ever appends to those, also when a later process
//     picks the session back up with --continue, so cutting one back to a
//     length is claude forgetting everything after it.
//
// Reverting is therefore a reset, a truncate and a cut log.

// Mark is one save point, carried by the chat entry that shows it.
type Mark struct {
	Commit  string           `json:"commit"`            // the task clone's HEAD at the point
	Message string           `json:"message,omitempty"` // the commit message the task was proposing then, if any
	Claude  map[string]int64 `json:"claude,omitempty"`  // transcript, relative to the claude dir → the length it had
}

// claudeOffsets measures every session transcript the task has. Paths are
// relative to the claude dir, so they survive being copied into a fork's.
func (t *Task) claudeOffsets() map[string]int64 {
	dir := t.claudeDir()
	paths, err := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
	if err != nil || len(paths) == 0 {
		return nil
	}
	lengths := map[string]int64{}
	for _, path := range paths {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(path); err == nil {
			lengths[filepath.ToSlash(rel)] = fi.Size()
		}
	}
	return lengths
}

// rewindClaude puts a claude dir back to what a mark measured: every
// transcript is cut to the length it had then, and one that did not exist yet
// goes. Nothing else of the dir is touched — its config and caches say
// nothing about the conversation.
//
// No claude may be running on it: a live process writes at the offset it holds
// and would tear a hole in the file behind it.
func rewindClaude(dir string, lengths map[string]int64) error {
	paths, err := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			continue
		}
		size, kept := lengths[filepath.ToSlash(rel)]
		if !kept {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if int64(len(data)) <= size {
			continue
		}
		// Back to the last whole line. Nothing is writing here now — the agent
		// was stopped first — but the length was taken at the end of a turn,
		// with claude alive and its transcript possibly part-way through a
		// line; those lines carry whole tool outputs and run to megabytes, which
		// is more than a write lands atomically. Half a line is the one thing
		// claude cannot read its own file past, so it is never cut on one.
		cut := bytes.LastIndexByte(data[:size], '\n') + 1
		if err := os.Truncate(path, int64(cut)); err != nil {
			return err
		}
	}
	return nil
}

// dirtyTree: the working tree has something no commit of the clone holds.
// Ignored files are not work (see the merge, which stages the same set).
func dirtyTree(repo string) (bool, error) {
	out, err := git(repo, "status", "--porcelain")
	return strings.TrimSpace(out) != "", err
}

// markSummary is how long a one-line summary of a run may be: a commit subject.
const markSummary = 200

// mark closes an agent run at a point the task can be put back to: what is in
// the working tree becomes a commit, and the log gets a save point holding
// that commit next to the length of every claude transcript.
//
// `by` names the point and heads the commit message: "Agent", with the summary
// of the run it gave in its TPS-DONE line, "Human" for what was in the tree
// before an agent was sent in, or "Start" for the point every task opens on. A
// run that changed nothing still gets a point, the conversation having moved
// even where the tree did not, and so does the start; a humans nothing is
// nothing at all.
func (t *Task) mark(by, summary string) {
	if !t.hasWorkspace() {
		return // in Plan, or finished: no tree to commit and nothing to come back to
	}
	repo := t.repoDir()
	message := by
	if summary = oneLine(summary, markSummary); summary != "" {
		message = by + ": " + summary
	}
	dirty, err := dirtyTree(repo)
	if err != nil {
		t.noteErr("reading the workspace failed, so no save point was made", err)
		return
	}
	if !dirty && by == "Human" {
		return
	}
	if dirty {
		if _, err := git(repo, "add", "-A"); err != nil {
			t.noteErr("committing the work so far failed", err)
			return
		}
		if _, err := git(repo, "commit", "--no-verify", "-m", message); err != nil {
			t.noteErr("committing the work so far failed", err)
			return
		}
	}
	head, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.noteErr("reading the workspace's commit failed", err)
		return
	}
	m := &Mark{Commit: head, Claude: t.claudeOffsets()}
	e := newEntry("mark")
	// Its own id, which UsePoint names it by; the tool
	// call ids entries otherwise carry cannot collide with it.
	e.ID = "mark-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	e.Text = message
	t.lock()
	m.Message = t.info.CommitMessage
	e.Mark = m
	t.p.m.saveL()
	t.unlock()
	t.addEntry(e)
	go t.refreshChanges()
}

// --- finding a point back in the log ---

// chatUpTo reads the chat log up to and including the entry that holds the
// named save point, and that entry. The bytes are what a revert leaves of
// the log and what a fork copies of it, and the rest is what either leaves
// behind (see nextPrompt).
func (t *Task) chatUpTo(id string) (log []byte, at int, point *ChatEntry, err error) {
	t.chatMu.Lock()
	defer t.chatMu.Unlock()
	data, err := os.ReadFile(t.chatFile())
	if err != nil {
		return nil, 0, nil, errors.New("this task's chat log is not there to work back through")
	}
	// SplitAfter keeps the newlines, so the lines up to and including the
	// point's join back into exactly the bytes the log is to be left with.
	lines := bytes.SplitAfter(data, []byte("\n"))
	for i, line := range lines {
		var e ChatEntry
		if json.Unmarshal(line, &e) == nil && e.ID == id && e.Mark != nil {
			return data, len(bytes.Join(lines[:i+1], nil)), &e, nil
		}
	}
	return nil, 0, nil, errors.New("that save point is no longer in the chat log")
}

// nextPrompt is the first thing the user said after a point: the message that
// set off the run being undone. Reverting and forking both hand it back to
// the composer, where it can be sent again as it is or said differently —
// having to retype it would be the one thing lost that nobody meant to lose.
func nextPrompt(rest []byte) string {
	for _, line := range bytes.Split(rest, []byte("\n")) {
		var e ChatEntry
		if json.Unmarshal(line, &e) == nil && e.K == "user" {
			return e.Text
		}
	}
	return ""
}

// --- using a point ---

// Use is what is asked of a save point. The two rewinds are separate things
// and either can be had alone: put the conversation back to the point and the
// agent works on today's tree with an older mind, put the tree back and it
// keeps the whole discussion of work that is no longer there. Both together
// is the plain undo. Forking does the same to a copy and leaves this task be.
type Use struct {
	Fork bool // to a second task, rather than to this one
	Chat bool // the chat log and claude's memory end at the point
	Work bool // the working tree holds the point's commit
}

// explains: exactly one of the two was rewound, so the agent's memory and the
// tree in front of it no longer agree and it has to be told which way. Rewind
// both and they agree as they did at the point: there is nothing to say.
func (u Use) explains() bool { return u.Chat != u.Work }

// UsePoint is the one way a save point is acted on. It answers with the task
// the state landed in (this one, or the fork it made) and with the message
// that set the undone run off, for the composer to offer back.
func (t *Task) UsePoint(id string, use Use) (tid, draft string, err error) {
	defer t.p.m.work()()
	if !use.Chat && !use.Work {
		return "", "", errors.New("Nothing to do: rewind the conversation, the code, or both")
	}
	if !t.hasWorkspace() {
		return "", "", errors.New("This task has no workspace to work from; pick it up first")
	}
	// A finished task keeps its conversation compressed; this reads from the
	// files, and the task is parked again by itself (see parkFinished).
	if err := t.unarchive(); err != nil {
		return "", "", err
	}
	log, at, point, err := t.chatUpTo(id)
	if err != nil {
		return "", "", err
	}
	if !gitOK(t.repoDir(), "cat-file", "-e", point.Mark.Commit+"^{commit}") {
		return "", "", errors.New("the commit this point was made at is no longer in the workspace")
	}
	if use.Fork {
		tid, err = t.fork(point, log, at, use)
	} else {
		tid, err = t.tid, t.revert(point, log[:at], use)
	}
	if err != nil {
		return tid, "", err
	}
	return tid, nextPrompt(log[at:]), nil
}

// revert puts this task back to the point: the working tree to the commit it
// holds, claude's memory to the length it had, and the log to the entry
// itself — whichever of those was asked for. What it undoes is gone: this is
// the dashboard's one destructive action that no patch or archive keeps a copy
// of.
//
// The workspace is moved rather than made anew, so what is in it but not in
// git — an ignored build directory, installed dependencies — is still there
// afterwards, which is most of why reverting is quick.
func (t *Task) revert(point *ChatEntry, upTo []byte, use Use) error {
	// Before anything is cut: a claude still running would write on past the
	// truncation, and would keep the memory this is undoing besides.
	t.stopAgent()
	if use.Work {
		if _, err := git(t.repoDir(), "reset", "--quiet", "--hard", point.Mark.Commit); err != nil {
			return err
		}
		if _, err := git(t.repoDir(), "clean", "--quiet", "-fd"); err != nil {
			return err
		}
	}
	if use.Chat {
		if err := rewindClaude(t.claudeDir(), point.Mark.Claude); err != nil {
			return err
		}
		t.chatMu.Lock()
		err := os.WriteFile(t.chatFile(), upTo, 0o644)
		t.chatMu.Unlock()
		if err != nil {
			return err
		}
		t.loadChat()
	}
	t.lock()
	t.info.CommitMessage = point.Mark.Message
	t.info.Conflicts = nil
	t.info.Pending = nil // what was waiting was waiting for a turn that is now undone
	if use.Chat {
		// A point from before the agent ever ran leaves no session to resume.
		t.info.Started = len(point.Mark.Claude) > 0
		t.info.Context = 0 // the conversation is another length now; the next turn measures it
	}
	if use.explains() {
		t.queueL("point", pointPrompt(use))
	}
	t.setPhaseL(PhaseHuman)
	t.unlock()
	// The one record of a revert there is, the rest having just been cut away.
	what := "the conversation and the working tree are"
	if !use.Work {
		what = "the conversation is"
	} else if !use.Chat {
		what = "the working tree is"
	}
	t.note(fmt.Sprintf("put back to %q: %s as at that point, and what came after is gone", point.Text, what))
	go t.refreshChanges()
	return nil
}

// --- forking ---

// forkNumber matches the ' #2' a fork's title ends in, so forking a fork
// numbers from the same stem instead of stacking suffixes.
var forkNumber = regexp.MustCompile(` #\d+$`)

// forkTitleL names a fork: the source's title with the lowest free number
// after it, so the board tells the two apart at a glance.
func (p *Project) forkTitleL(title string) string {
	stem := cmp.Or(strings.TrimSpace(forkNumber.ReplaceAllString(strings.TrimSpace(title), "")), "Task")
	taken := map[string]bool{}
	for _, info := range p.info.Tasks {
		taken[info.Title] = true
	}
	for n := 2; ; n++ {
		if name := fmt.Sprintf("%s #%d", stem, n); !taken[name] {
			return name
		}
	}
}

// newForkL makes the fork's task: the source's settings, its description, and
// a title of its own. It waits for a human — a fork is something to look at
// before it is sent anywhere.
func (p *Project) newForkL(src *TaskInfo, title string) *Task {
	tid := strconv.Itoa(p.info.NextTask)
	p.info.NextTask++
	info := &TaskInfo{
		Title: title, Description: src.Description, Model: src.Model,
		ReviewModel: src.ReviewModel, OnReady: src.OnReady, OnAccept: src.OnAccept, ReviewLoops: src.ReviewLoops,
		Phase: PhaseHuman, PhaseAt: time.Now().UnixMilli(),
		TitleAsked: true,
	}
	if src.Budget != nil {
		budget := *src.Budget
		info.Budget = &budget
	}
	p.info.Tasks[tid] = info
	t := newTask(p, tid, info)
	p.tasks[tid] = t
	p.touchL()
	p.m.saveL()
	t.publishL()
	return t
}

// fork is the same rewind, done to a copy: a new task in the same project that
// starts where this one is, with whichever of the two put back to the point.
// This task is left exactly as it is — a fork is the answer to "what if",
// where reverting is "no".
func (t *Task) fork(point *ChatEntry, log []byte, at int, use Use) (string, error) {
	mark := point.Mark
	t.lock()
	src := *t.info
	title := t.p.forkTitleL(src.Title)
	f := t.p.newForkL(&src, title)
	t.unlock()
	// What the fork's one commit is called: the merge message the task was
	// proposing at the point, or the one it is proposing now, or its name.
	message := cmp.Or(strings.TrimSpace(mark.Message), strings.TrimSpace(src.CommitMessage), title)
	if err := os.MkdirAll(f.dir(), 0o755); err != nil {
		return f.tid, err
	}
	// The work first, as a patch beside the fork's task dir: making the
	// workspace plants it onto the branch as it is now, the way picking a
	// closed task back up does (see ensureWorkspace). The tree left alone is
	// this task's as it stands, the point's being what rewinding it means.
	from := "HEAD"
	if use.Work {
		from = mark.Commit
	}
	if err := t.forkPatch(f, from); err != nil {
		return f.tid, err
	}
	// The log the fork opens on, and what the user attached, which it refers to
	// by the path those files are read at.
	if !use.Chat {
		at = len(log)
	}
	if err := os.WriteFile(f.chatFile(), log[:at], 0o644); err != nil {
		return f.tid, err
	}
	if err := copyTree(t.uploadsDir(), f.uploadsDir()); err != nil {
		return f.tid, err
	}
	if err := copyTree(t.claudeDir(), f.claudeDir()); err != nil {
		return f.tid, err
	}
	if use.Chat {
		if err := rewindClaude(f.claudeDir(), mark.Claude); err != nil {
			return f.tid, err
		}
	}
	f.lock()
	f.info.Started = len(f.claudeOffsets()) > 0
	f.p.m.saveL()
	f.unlock()
	f.note(fmt.Sprintf("forked from %q, at its save point %q", src.Title, oneLine(point.Text, 60)))
	if err := f.ensureWorkspace(); err != nil {
		return f.tid, err
	}
	// One commit of the work, as the fork's starting point.
	if f.hasWorkspace() {
		if _, err := git(f.repoDir(), "add", "-A"); err != nil {
			return f.tid, err
		}
		if !gitOK(f.repoDir(), "diff", "--cached", "--quiet") {
			if _, err := git(f.repoDir(), "commit", "--no-verify", "-m", message); err != nil {
				return f.tid, err
			}
		}
	}
	f.lock()
	// Planting the patch left the note a closed task gets when it is picked
	// back up; this is a fork, and what it is comes from pointPrompt instead.
	f.dropPendingL("reopened")
	if use.explains() {
		f.queueL("point", pointPrompt(use))
	}
	f.unlock()
	f.loadChat()
	t.note(fmt.Sprintf("forked from this point into %q", title))
	go f.refreshChanges()
	return f.tid, nil
}

// forkPatch writes the work up to a point as the fork's patch: everything the
// point's commit has that the branch it grew from does not, in the format
// ensureWorkspace plants (see savePatch).
func (t *Task) forkPatch(f *Task, commit string) error {
	repo := t.repoDir()
	base, err := git(repo, "merge-base", commit, "origin/"+t.p.defaultBranch)
	if err != nil {
		return err
	}
	// Not through git(), which trims: a patch is bytes, and git apply wants
	// the last line ended.
	r, err := runCmd([]string{"git", "-C", repo, "diff", "--binary", "--full-index", "--no-renames", base, commit}, RunOpts{})
	if err != nil {
		return err
	}
	if r.Out == "" {
		return nil
	}
	return os.WriteFile(f.patchFile(), []byte("base "+base+"\n\n"+r.Out), 0o644)
}

// copyTree copies a directory, if it is there at all.
func copyTree(from, to string) error {
	if !exists(from) {
		return nil
	}
	_, err := runCmd([]string{"cp", "-a", from, to}, RunOpts{})
	return err
}
