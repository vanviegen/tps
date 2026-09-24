package daemon

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
//   - the work, as a commit on the steps branch of the task's own clone (see
//     step; a merge squashes the work back into one commit);
//   - the chat log, as the bytes up to the entry the point is;
//   - what the agent remembers, as the length of each of its session
//     transcripts. It only ever appends to those, also when a later process
//     picks the session back up, so cutting one back to a length is the agent
//     forgetting everything after it.
//
// Reverting is therefore a reset, a truncate and a cut log.

// Mark is one save point, carried by the chat entry that shows it.
type Mark struct {
	Commit string `json:"commit"` // the step the point made on stepsBranch
	// Transcript, relative to the agent's state dir → the length it had. The
	// JSON name is the one every save point on disk carries: a point whose
	// lengths cannot be read back deletes transcripts instead of cutting them.
	Sessions map[string]int64 `json:"claude,omitempty"`
}

// How long the transcripts must hold still to count as written out, and how
// long a save point may be held up insisting on it.
const (
	transcriptQuiet = 300 * time.Millisecond
	transcriptWait  = 5 * time.Second
)

// settleTranscripts waits for a running agent to finish writing down the turn it
// has just reported over. The message a turn ends on reaches the transcript
// some tens of milliseconds after the stream says the turn is done, and a
// point measured in that window holds a length from before the agent's last
// words: reverting to it would cut away the very answer it was made at, and
// the agent would pick the conversation back up without the analysis the user
// is answering. So the lengths are only taken once the files have gone quiet.
func (t *Task) settleTranscripts(p Provider) {
	t.lock()
	running := t.session != nil
	t.unlock()
	if !running {
		return // nothing is writing; whatever is there is all there is
	}
	lengths := t.transcripts(p)
	for deadline := time.Now().Add(transcriptWait); time.Now().Before(deadline); {
		time.Sleep(transcriptQuiet)
		now := t.transcripts(p)
		if maps.Equal(now, lengths) {
			return
		}
		lengths = now
	}
}

// transcripts measures every session transcript the task has for this agent.
// Paths are relative to its state dir, so they survive being copied into a
// fork's.
func (t *Task) transcripts(p Provider) map[string]int64 {
	dir := t.agentDir(p)
	paths, err := filepath.Glob(filepath.Join(dir, p.Transcripts()))
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

// rewindTranscripts puts an agent's state dir back to what a mark measured: every
// transcript is cut to the length it had then, and one that did not exist yet
// goes. Nothing else of the dir is touched — its config and caches say
// nothing about the conversation.
//
// No agent may be running on it: a live process writes at the offset it holds
// and would tear a hole in the file behind it.
func rewindTranscripts(p Provider, dir string, lengths map[string]int64) error {
	paths, err := filepath.Glob(filepath.Join(dir, p.Transcripts()))
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
		// with the agent alive and its transcript possibly part-way through a
		// line; those lines carry whole tool outputs and run to megabytes, which
		// is more than a write lands atomically. Half a line is the one thing an
		// agent cannot read its own file past, so it is never cut on one.
		cut := bytes.LastIndexByte(data[:size], '\n') + 1
		if err := os.Truncate(path, int64(cut)); err != nil {
			return err
		}
	}
	return nil
}

// stepsBranch is where a task's clone keeps its steps: a commit of the whole
// tree at every save point, each on top of the one before, and a merge with
// the new base wherever a rebase moved it (see plant). The branch checked out
// stays at the base, so that what the tree differs from it in is the task's
// work as a whole, uncommitted.
const stepsBranch = "tps-steps"

// lastStep is the tip of stepsBranch, or — before the first step — HEAD.
func lastStep(repo string) (string, error) {
	if tip, err := git(repo, "rev-parse", "--verify", "--quiet", stepsBranch); err == nil {
		return tip, nil
	}
	return git(repo, "rev-parse", "HEAD")
}

// writeTree makes a tree object of the working tree, untracked files
// included. Ignored files are not work (see the merge, which stages the same
// set).
func (t *Task) writeTree(repo string) (string, error) {
	env, cleanup, err := t.stageAll(repo)
	if err != nil {
		return "", err
	}
	defer cleanup()
	r, err := runCmd([]string{"git", "-C", repo, "write-tree"}, RunOpts{Env: env})
	return strings.TrimSpace(r.Out), err
}

// dirtyTree: the working tree has something the last step does not hold.
func (t *Task) dirtyTree(repo string) (bool, error) {
	tree, err := t.writeTree(repo)
	if err != nil {
		return false, err
	}
	last, err := lastStep(repo)
	return !gitOK(repo, "diff", "--quiet", last, tree), err
}

// step records the working tree on stepsBranch — as a commit of its own if it
// holds anything the last step does not — and puts HEAD and the index back on
// the base, the tree untouched: whatever was committed or staged in the
// workspace itself is unstaged work again. A base the steps do not have yet, a
// rebase's, becomes the commit's second parent. Answers the branch's tip, and
// whether that is a new commit.
func (t *Task) step(repo, message string) (string, bool, error) {
	base, err := t.base(repo, "HEAD")
	if err != nil {
		return "", false, err
	}
	last, err := lastStep(repo)
	if err != nil {
		return "", false, err
	}
	tree, err := t.writeTree(repo)
	if err != nil {
		return "", false, err
	}
	parents := []string{"-p", last}
	if !gitOK(repo, "merge-base", "--is-ancestor", base, last) {
		parents = append(parents, "-p", base)
	}
	head, made := last, len(parents) > 2 || !gitOK(repo, "diff", "--quiet", last, tree)
	if made {
		if head, err = git(repo, append([]string{"commit-tree", tree, "-m", message}, parents...)...); err != nil {
			return "", false, err
		}
	}
	if _, err := git(repo, "update-ref", "refs/heads/"+stepsBranch, head); err != nil {
		return "", false, err
	}
	_, err = git(repo, "reset", "--quiet", base)
	return head, made, err
}

// markSummary is how long a one-line summary of a run may be: a commit subject.
const markSummary = 200

// mark closes an agent run at a point the task can be put back to: the working
// tree becomes a step (see step), and the log gets a save point holding that
// step next to the length of every transcript the agent has.
//
// `by` names the point and heads the commit message: "Agent", with the summary
// of the run it gave in its TPS-DONE line, "Human" for what was in the tree
// before an agent was sent in, "Rebase" for a merge of the latest branch (see
// Rebase), or "Start" for the point every task opens on. A run that changed
// nothing still gets a point, the conversation having moved even where the
// tree did not, and so does the start; a human's or a rebase's nothing is
// nothing at all.
func (t *Task) mark(by, summary string) error {
	if !t.hasWorkspace() {
		return nil // in Plan, or finished: no tree to commit and nothing to come back to
	}
	message := by
	if summary = oneLine(summary, markSummary); summary != "" {
		message = by + ": " + summary
	}
	head, made, err := t.step(t.repoDir(), message)
	if err != nil {
		t.noteErr("committing the work so far failed, so no save point was made", err)
		return err
	}
	if !made && (by == "Human" || by == "Rebase") {
		return nil
	}
	agent, _ := t.agent()
	t.settleTranscripts(agent)
	m := &Mark{Commit: head, Sessions: t.transcripts(agent)}
	e := newEntry("mark")
	// Its own id, which UsePoint names it by; the tool
	// call ids entries otherwise carry cannot collide with it.
	e.ID = "mark-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	e.Text = message
	e.Mark = m
	t.addEntry(e)
	go t.refreshChanges()
	return nil
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
	Chat bool // the chat log and the agent's memory end at the point
	Work bool // the working tree and the steps are back at the point's step
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
	if use.Work && !gitOK(t.repoDir(), "cat-file", "-e", point.Mark.Commit+"^{commit}") {
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

// revert puts this task back to the point: the working tree and the steps to
// the step it holds and HEAD to that step's base, the agent's memory to the
// length it had, and the log to the entry itself — whichever of those was
// asked for. What it undoes is gone: this is the dashboard's one destructive
// action that no patch or archive keeps a copy of.
//
// The workspace is moved rather than made anew, so what is in it but not in
// git — an ignored build directory, installed dependencies — is still there
// afterwards, which is most of why reverting is quick.
func (t *Task) revert(point *ChatEntry, upTo []byte, use Use) error {
	// Before anything is cut: an agent still running would write on past the
	// truncation, and would keep the memory this is undoing besides.
	t.stopAgent()
	if use.Work {
		repo, step := t.repoDir(), point.Mark.Commit
		base, err := t.base(repo, step)
		if err != nil {
			return err
		}
		for _, args := range [][]string{{"reset", "--quiet", "--hard", step}, {"clean", "--quiet", "-fd"}, {"update-ref", "refs/heads/" + stepsBranch, step}, {"reset", "--quiet", base}} {
			if _, err := git(repo, args...); err != nil {
				return err
			}
		}
	}
	if use.Chat {
		agent, _ := t.agent()
		if err := rewindTranscripts(agent, t.agentDir(agent), point.Mark.Sessions); err != nil {
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
	// The commit message is not put back here: it is a file of the tree the
	// point holds, and the refresh below reads and publishes it (see
	// commitMessageFile).
	t.info.Ready = false
	t.info.Conflicts = nil
	t.info.Pending = nil // what was waiting was waiting for a turn that is now undone
	if use.Chat {
		// A point from before the agent ever ran leaves no session to resume.
		t.info.Started = len(point.Mark.Sessions) > 0
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
		ReviewModel: src.ReviewModel, OnReady: src.OnReady, OnAccept: src.OnAccept,
		ReviewLoops: copyPtr(src.ReviewLoops), Budget: copyPtr(src.Budget),
		Phase: PhaseHuman, PhaseAt: time.Now().UnixMilli(),
		TitleAsked: true,
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
	if err := os.MkdirAll(f.dir(), 0o755); err != nil {
		return f.tid, err
	}
	// The work first, as a patch beside the fork's task dir: making the
	// workspace plants it onto the branch as it is now, the way picking a
	// closed task back up does (see ensureWorkspace). The tree left alone is
	// this task's as it stands, the point's step being what rewinding it means.
	of, work := mark.Commit, mark.Commit
	if !use.Work {
		tree, err := t.writeTree(t.repoDir())
		if err != nil {
			return f.tid, err
		}
		of, work = "HEAD", tree
	}
	if err := t.forkPatch(f, of, work); err != nil {
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
	agent, _ := t.agent()
	if err := copyTree(t.agentDir(agent), f.agentDir(agent)); err != nil {
		return f.tid, err
	}
	if use.Chat {
		if err := rewindTranscripts(agent, f.agentDir(agent), mark.Sessions); err != nil {
			return f.tid, err
		}
	}
	f.lock()
	f.info.Started = len(f.transcripts(agent)) > 0
	f.p.m.saveL()
	f.unlock()
	f.note(fmt.Sprintf("forked from %q, at its save point %q", src.Title, oneLine(point.Text, 60)))
	if err := f.ensureWorkspace(); err != nil {
		return f.tid, err
	}
	// One step of the work, as the fork's starting point, under the message
	// the work came with.
	if f.hasWorkspace() {
		if _, _, err := f.step(f.repoDir(), cmp.Or(f.commitMessage(), title)); err != nil {
			return f.tid, err
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

// forkPatch writes work as the fork's patch — a step, or a tree of the
// workspace as it stands — against the base of the commit named by `of`, in
// the format ensureWorkspace plants (see savePatch).
func (t *Task) forkPatch(f *Task, of, work string) error {
	repo := t.repoDir()
	base, err := t.base(repo, of)
	if err != nil {
		return err
	}
	// Not through git(), which trims: a patch is bytes, and git apply wants
	// the last line ended.
	r, err := runCmd([]string{"git", "-C", repo, "diff", "--binary", "--full-index", "--no-renames", base, work}, RunOpts{})
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
