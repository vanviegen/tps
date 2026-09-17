package daemon

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vanviegen/tps/hub"
)

type Phase string

const (
	PhasePlan   Phase = "plan"
	PhaseAgent  Phase = "agent"
	PhaseHuman  Phase = "human"
	PhaseMuted  Phase = "muted"  // waiting for a human too, but parked: out of the sidebar and at the foot of its column
	PhaseReview Phase = "review" // a second agent is reading the work over before it is handed on (see review.go)
	PhaseMerge  Phase = "merge"  // TPS is merging: replaying the work onto the branch, or an agent resolving conflicts in it
	PhaseDone   Phase = "done"   // merged: its work is on the branch and its workspace is gone
	PhaseClosed Phase = "closed" // closed without merging: its work is kept as a patch, off the branch, and its workspace is gone
)

var phases = []Phase{PhasePlan, PhaseAgent, PhaseReview, PhaseHuman, PhaseMuted, PhaseMerge, PhaseDone, PhaseClosed}

type WorkStatus string

const (
	StatusDown     WorkStatus = "down"
	StatusBuilding WorkStatus = "building"
	StatusStarting WorkStatus = "starting"
	StatusUp       WorkStatus = "up"
	StatusStopping WorkStatus = "stopping"
	StatusError    WorkStatus = "error"
)

const (
	idleShutdown  = 15 * time.Minute
	containerfile = "Containerfile.dev"
)

// Waiting out one of claude's usage limits (see armLimitL): the agent goes
// back in a minute past the reset it named, and a reset further out than a
// day, or one that keeps coming back, is left to the user instead.
const (
	limitSlack    = time.Minute
	limitMaxWait  = 24 * time.Hour
	maxLimitWaits = 12
)

// Answer is what a task does at one of the three moments where finished work
// can go more than one way (see review.go): hand it to the user, merge it, or
// have it read over first. Empty is the first of them, which is what a task
// that says nothing does.
type Answer string

const (
	AnswerHuman  Answer = "human"  // assign the task to the user
	AnswerMerge  Answer = "merge"  // merge it, with the commit message the agent wrote
	AnswerReview Answer = "review" // have a second agent read the work over first
)

// What a task that says nothing does: work an agent calls finished is read over
// before anyone is asked to look at it, and what the review makes of it is the
// user's to act on.
const (
	defaultOnReady  = AnswerReview
	defaultOnAccept = AnswerHuman
)

// parseAnswer reads an answer as it arrives from a dashboard, and reports
// whether it is one of those the question offers.
func parseAnswer(raw any, offered ...Answer) (Answer, bool) {
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	for _, a := range offered {
		if Answer(s) == a {
			return a, true
		}
	}
	return "", false
}

// TaskInfo is the persisted part of a task (in projects.json).
type TaskInfo struct {
	Title         string         `json:"title"`
	Description   string         `json:"description"`
	Model         string         `json:"model"`
	OnReady       Answer         `json:"onReady,omitempty"`     // what becomes of work the agent reports ready (see review.go)
	OnAccept      Answer         `json:"onAccept,omitempty"`    // and of work a review accepts
	ReviewLoops   int            `json:"reviewLoops,omitempty"` // how often a review asking for changes may send the work back to the agent
	ReviewLoop    int            `json:"reviewLoop,omitempty"`  // how often it has, since the user last said something
	ReviewModel   string         `json:"reviewModel,omitempty"` // the model the reviewer runs on; empty is claude's own default
	Review        string         `json:"review,omitempty"`      // the last review's feedback, waiting for the user (see review.go)
	Phase         Phase          `json:"phase"`
	Started       bool           `json:"started,omitempty"`       // a claude session exists in the task's claude dir
	CommitMessage string         `json:"commitMessage,omitempty"` // the message the agent proposed for the merge, last time it reported one
	Ready         bool           `json:"ready,omitempty"`         // that report still stands: nothing has been asked of the task since (see noteReadyL)
	Spent         float64        `json:"spent,omitempty"`         // USD spent on agent runs so far
	Budget        *float64       `json:"budget,omitempty"`        // USD limit; the task is parked when spending reaches it
	Context       int64          `json:"context,omitempty"`       // tokens the conversation came to at the agent's last turn
	Window        *ContextWindow `json:"window,omitempty"`        // and what claude's window holds besides it (see context.go)
	TitleAsked    bool           `json:"titleAsked,omitempty"`    // claude has been asked to name this task (once is enough)
	PhaseAt       int64          `json:"phaseAt,omitempty"`       // ms epoch of the last phase change; boards show the freshest first
	StartAfter    []string       `json:"startAfter,omitempty"`    // tids this task follows: it leaves Plan by itself once they are all done
	Conflicts     []string       `json:"conflicts,omitempty"`     // files the last replay onto the branch could not merge cleanly (see plant); until a clean one, or the merge
	LimitUntil    int64          `json:"limitUntil,omitempty"`    // ms epoch the agent goes back in at, waiting out a usage limit (see armLimitL)

	// Pending is what the agent is told the next time it is sent in: things
	// that happened to its workspace while it wasn't running.
	Pending []PendingNote `json:"pending,omitempty"`
}

// PendingNote is one message waiting for the agent's next turn. A key makes it
// replaceable (the same thing happening twice says it once) and droppable (see
// dropPendingL), for notes that can go stale before they are ever read.
type PendingNote struct {
	Key  string `json:"key,omitempty"`
	Text string `json:"text"`
}

// flight is one in-progress operation shared by everyone who asks for it
// meanwhile (like a memoized promise).
type flight[T any] struct {
	done chan struct{}
	val  T
	err  error
}

// Task: its workspace is a plain clone of the project repo (git hardlinks the
// object store, so cloning is nearly free and safe: object files are never
// modified in place), created when the task leaves the plan phase and kept
// until the task finishes or is discarded back to plan. A sibling dir holds
// the claude conversation state, another file the condensed chat log; a
// finished task has those compressed into one archive, and a closed one its
// work as a patch beside it (see park).
//
// Methods with an L suffix expect the manager lock to be held; the others
// take it themselves and never hold it across I/O.
type Task struct {
	p            *Project
	tid          string
	info         *TaskInfo
	status       WorkStatus
	statusDetail string
	container    *Container
	viewers      int
	lastActivity time.Time
	codeWanted   bool  // a dashboard holds the task open: VS Code should be up
	codeUp       bool  // code-server runs in the container
	codeSyncing  bool  // a syncCode is under way
	codeStart    int64 // ms epoch of the code-server start: a new one means VS Code must reload
	codeError    string
	live         map[int]portProbe // by container port: what answers there (see checkLive)
	checkingLive bool
	refreshing   bool // a changes overview is being computed
	autoStarting bool // an auto-start is under way, so it isn't started twice
	behind       int  // commits on the default branch the workspace doesn't have yet (see refreshBehind)

	session       *ChatSession
	sessionFlight *flight[*ChatSession]
	sessionBudget *float64 // the budget setting the running claude was started under
	sessionReview bool     // and whether it is the reviewer rather than the task's own agent (see review.go)
	upFlight      *flight[*Container]
	lastTag       string // image tag of the Containerfile the container was brought up for
	stopping      bool
	doneNudges    int         // turns in a row the agent was sent back in for a missing TPS-DONE line
	limitTimer    *time.Timer // running while the task waits out a usage limit (see armLimitL)
	limitWaits    int         // waits in a row that ran straight into the limit again
	chatMu        sync.Mutex
	cloneMu       sync.Mutex // one workspace at a time: two messages can want one at once

	declared      []declaredService // the services Containerfile.dev names, known along with the container
	services      []serviceState    // their state and that of the ad hoc ones, as last read (see services.go)
	logsPublished map[string]bool   // the services whose output tail the dashboard has
}

func newTask(p *Project, tid string, info *TaskInfo) *Task {
	return &Task{p: p, tid: tid, info: info, status: StatusDown, lastActivity: time.Now()}
}

func (t *Task) key() string           { return t.p.pid + "/" + t.tid }
func (t *Task) dir() string           { return filepath.Join(t.p.tasksDir(), t.tid) }
func (t *Task) repoDir() string       { return filepath.Join(t.dir(), "repo") }
func (t *Task) claudeDir() string     { return filepath.Join(t.dir(), "claude") }
func (t *Task) chatFile() string      { return filepath.Join(t.dir(), "chat.jsonl") }
func (t *Task) patchFile() string     { return filepath.Join(t.dir(), "work.patch") }
func (t *Task) archiveFile() string   { return filepath.Join(t.dir(), "archive.tar.gz") }
func (t *Task) containerName() string { return "tps-" + t.p.pid + "-" + t.tid }
func (t *Task) workingL() bool        { return t.session != nil && t.session.TurnActive() }
func (t *Task) touchL()               { t.lastActivity = time.Now() }

// hasWorkspace: the task has a clone to work in. A bare directory is not one
// and must not pass for one: podman makes the mount point back whenever a
// container starts without one there — after a merge dropped it, above all —
// and a removal that could not free the path leaves what it did not get to.
// Either would be taken for a workspace and fail as one, every git command in
// it reporting that the task's repo is not a repository.
func (t *Task) hasWorkspace() bool { return exists(filepath.Join(t.repoDir(), ".git")) }

// onReadyL and onAcceptL: what this task does with work the agent reports
// ready, and with work a review accepts.
func (t *Task) onReadyL() Answer  { return cmp.Or(t.info.OnReady, defaultOnReady) }
func (t *Task) onAcceptL() Answer { return cmp.Or(t.info.OnAccept, defaultOnAccept) }

// conversationPart names the share of the window the conversation itself takes:
// the one part claude does not name, because it is everything it did not.
const conversationPart = "This conversation"

// contextL is the agent's context window as the dashboard draws it: what the
// last turn really sent, where that stops, and what it is made of. The parts
// were measured on their own and the turn's total on its own, so the two can
// disagree by a little; what a turn sent is what was really paid for, so the
// parts are laid inside that and the conversation is whatever is left over —
// which is also the only part that grows, and the only one compacting takes
// back. Nil until both have been measured, there being no window to draw then.
func (t *Task) contextL() any {
	w := t.info.Window
	if t.info.Context <= 0 || w == nil || w.Limit <= 0 {
		return nil
	}
	left := t.info.Context
	parts := make([]ContextPart, 0, len(w.Parts)+1)
	for _, p := range w.Parts {
		size := min(left, p.Tokens)
		parts = append(parts, ContextPart{Name: p.Name, Tokens: size})
		left -= size
	}
	parts = append(parts, ContextPart{Name: conversationPart, Tokens: left})
	return map[string]any{"used": t.info.Context, "limit": w.Limit, "parts": parts}
}

// agentPhaseL: an agent may be at work — on the task itself, on reviewing it,
// or on merging it — so nothing is expected of the user meanwhile.
func (t *Task) agentPhaseL() bool {
	return t.info.Phase == PhaseAgent || t.info.Phase == PhaseReview || t.info.Phase == PhaseMerge
}

func (t *Task) lock()   { t.p.m.mu.Lock() }
func (t *Task) unlock() { t.p.m.mu.Unlock() }

func (t *Task) pubL(field string, value any) {
	t.p.m.hub.Set([]string{"projects", t.p.pid, "tasks", t.tid, field}, value)
}

func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (t *Task) publishL() {
	t.pubL("title", t.info.Title)
	t.pubL("description", t.info.Description)
	t.pubL("model", t.info.Model)
	t.pubL("onReady", string(t.onReadyL()))
	t.pubL("onAccept", string(t.onAcceptL()))
	t.pubL("reviewLoops", t.info.ReviewLoops)
	t.pubL("reviewModel", nonEmpty(t.info.ReviewModel))
	t.pubL("review", nonEmpty(t.info.Review))
	t.pubL("phase", t.info.Phase)
	t.pubL("phaseAt", t.info.PhaseAt)
	t.pubL("commitMessage", nonEmpty(t.info.CommitMessage))
	if t.info.Ready {
		t.pubL("ready", true)
	} else {
		t.pubL("ready", nil)
	}
	if t.info.Spent > 0 {
		t.pubL("spent", t.info.Spent)
	} else {
		t.pubL("spent", nil)
	}
	t.pubL("budget", optional(t.info.Budget))
	t.pubL("context", t.contextL())
	if len(t.info.StartAfter) > 0 {
		t.pubL("startAfter", t.info.StartAfter)
	} else {
		t.pubL("startAfter", nil)
	}
	if len(t.info.Conflicts) > 0 {
		t.pubL("conflicts", t.info.Conflicts)
	} else {
		t.pubL("conflicts", nil)
	}
	// Whether there is a workspace to open at all: a task in Plan has none yet,
	// a finished one no longer.
	if t.hasWorkspace() {
		t.pubL("worktree", true)
	} else {
		t.pubL("worktree", nil)
	}
	if t.behind > 0 {
		t.pubL("behind", t.behind)
	} else {
		t.pubL("behind", nil)
	}
	if t.info.LimitUntil > 0 {
		t.pubL("limitUntil", t.info.LimitUntil)
	} else {
		t.pubL("limitUntil", nil)
	}
	t.pubL("status", t.status)
	t.pubL("statusDetail", t.statusDetail)
	t.pubL("working", t.workingL())
	t.pubL("ports", t.portsL())
	t.pubL("services", t.servicesL())
	// The port is there while VS Code is: its start time comes along, so a
	// dashboard can tell a restarted code-server (which needs a reload) from
	// the one it was talking to.
	if t.status == StatusUp && t.container != nil && t.codeUp {
		t.pubL("codePort", t.container.CodePort)
		t.pubL("codeStart", t.codeStart)
	} else {
		t.pubL("codePort", nil)
		t.pubL("codeStart", nil)
	}
	t.pubL("codeError", nonEmpty(t.codeError))
}

// parseBudget reads a spending limit as it arrives from a dashboard: a number
// or the text of a number field. Anything that is not a limit (empty, zero,
// nonsense) means no limit at all.
func parseBudget(raw any) *float64 {
	var budget float64
	switch v := raw.(type) {
	case float64:
		budget = v
	case string:
		fmt.Sscanf(strings.TrimSpace(v), "%g", &budget)
	}
	if budget <= 0 || math.IsInf(budget, 0) {
		return nil
	}
	return &budget
}

// maxReviewLoops caps how often a review may send the work back: a limit is
// there to end the back-and-forth, and one this long has stopped being
// something to leave running.
const maxReviewLoops = 3

// parseLoops reads that limit as it arrives from a dashboard: a number or the
// text of one. Anything that is not a count means none.
func parseLoops(raw any) int {
	var loops float64
	switch v := raw.(type) {
	case float64:
		loops = v
	case string:
		fmt.Sscanf(strings.TrimSpace(v), "%g", &loops)
	}
	return min(max(int(loops), 0), maxReviewLoops)
}

// optional turns a nil pointer into nil (deleting the field) and otherwise the value.
func optional[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

func (t *Task) setStatusL(status WorkStatus, detail string) {
	t.status, t.statusDetail = status, detail
	if status != StatusUp {
		t.live = nil
	}
	t.publishL()
}

func (t *Task) setPhaseL(phase Phase) {
	if phase != PhaseAgent && phase != PhaseMerge { // a task leaving the agent (the review included) waits for nothing
		t.clearLimitL()
	}
	if phase != PhaseHuman && phase != PhaseMuted { // sent anywhere at all, the task is no longer one reported done
		t.info.Ready = false
	}
	t.info.Phase = phase
	t.info.PhaseAt = time.Now().UnixMilli()
	t.p.touchL()
	t.p.m.saveL()
	t.publishL()
	t.p.autoStartL() // a task reaching Done may be the last one another was waiting for
	go t.syncCode()  // a workspace it just got (or lost) changes what VS Code can show
}

// --- chat log (no manager lock needed) ---

func (t *Task) addEntry(e *ChatEntry) {
	t.p.m.hub.Chat(t.key(), hub.Entry{ID: e.ID, V: e})
	t.appendChat(e)
}

// updateEntry re-sends a changed entry (a tool call that got its result); it replaces by id.
func (t *Task) updateEntry(e *ChatEntry) {
	t.p.m.hub.ChatUpdate(t.key(), hub.Entry{ID: e.ID, V: e})
	t.appendChat(e) // appended again; loadChat keeps the last version per id
}

func (t *Task) appendChat(e *ChatEntry) {
	if !exists(t.dir()) {
		return
	}
	raw, _ := json.Marshal(e)
	t.chatMu.Lock()
	defer t.chatMu.Unlock()
	f, err := os.OpenFile(t.chatFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}

func (t *Task) note(text string, detail ...string) {
	e := newEntry("note")
	e.Text = text
	if len(detail) > 0 {
		e.Detail = detail[0]
	}
	t.addEntry(e)
}

func (t *Task) noteErr(prefix string, err error) { t.note(prefix + ": " + err.Error()) }

// noteReadyL records work being reported ready to merge: the note stays in the
// log at the moment it happened, and the flag says the report still stands —
// until something is asked of the task again (see setPhaseL), which is what
// tells "the agent is done with this" from "the agent once said so". The
// message it proposed outlives both, as what the merge dialog opens with.
// Expects the lock.
func (t *Task) noteReadyL(text string) {
	t.info.Ready = true
	e := newEntry("note")
	e.Text, e.Ready = text, true
	t.addEntry(e)
}

// queueL saves something for the agent to be told the next time it is sent in,
// ahead of whatever sends it (see kick). Things happen to a workspace while no
// agent runs in it — it is rebased, its image falls back to the default one —
// and this is how the agent hears of them: not by interrupting whoever is
// working now, and not by being forgotten either. A note with a key replaces
// the one that key already had, so the same thing happening twice is said once.
func (t *Task) queueL(key, text string) {
	if key != "" {
		for i, n := range t.info.Pending {
			if n.Key == key {
				t.info.Pending[i].Text = text
				t.p.m.saveL()
				return
			}
		}
	}
	t.info.Pending = append(t.info.Pending, PendingNote{Key: key, Text: text})
	t.p.m.saveL()
}

// queue is queueL for a caller that holds no lock.
func (t *Task) queue(key, text string) {
	t.lock()
	defer t.unlock()
	t.queueL(key, text)
}

// dropPendingL takes back a note that never got read and no longer holds.
func (t *Task) dropPendingL(key string) {
	kept := t.info.Pending[:0]
	for _, n := range t.info.Pending {
		if n.Key != key {
			kept = append(kept, n)
		}
	}
	if len(kept) != len(t.info.Pending) {
		t.info.Pending = kept
		t.p.m.saveL()
	}
}

// takePendingL puts the waiting notes in front of the message the agent is
// about to get, and clears them.
func (t *Task) takePendingL(text string) string {
	if len(t.info.Pending) == 0 {
		return text
	}
	parts := make([]string, 0, len(t.info.Pending)+1)
	for _, n := range t.info.Pending {
		parts = append(parts, n.Text)
	}
	t.info.Pending = nil
	t.p.m.saveL()
	if strings.TrimSpace(text) != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n")
}

// loadChat publishes the chat log.
func (t *Task) loadChat() { t.p.m.hub.SetChat(t.key(), t.readChat()) }

// readChat reads the chat log, the last version of each entry: the file, or —
// for a finished task, whose data is compressed — the copy in its archive,
// followed by whatever was said since (a note made then is appended beside
// the archive; see unarchive).
func (t *Task) readChat() []hub.Entry {
	var readers []io.Reader
	if exists(t.archiveFile()) {
		if r, err := runCmd([]string{"tar", "-xOzf", t.archiveFile(), "chat.jsonl"}, RunOpts{}); err == nil {
			readers = append(readers, strings.NewReader(r.Out))
		}
	}
	if f, err := os.Open(t.chatFile()); err == nil {
		defer f.Close()
		readers = append(readers, f)
	}
	if len(readers) == 0 {
		return nil
	}
	var entries []hub.Entry
	byID := map[string]int{}
	sc := bufio.NewScanner(io.MultiReader(readers...))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var head struct {
			ID string `json:"id"`
		}
		if !json.Valid(line) || json.Unmarshal(line, &head) != nil {
			continue
		}
		e := hub.Entry{ID: head.ID, V: json.RawMessage(strings.Clone(string(line)))}
		if i, ok := byID[head.ID]; ok && head.ID != "" {
			entries[i] = e
		} else {
			if head.ID != "" {
				byID[head.ID] = len(entries)
			}
			entries = append(entries, e)
		}
	}
	return entries
}

// --- settings ---

func (t *Task) Update(partial map[string]any) error {
	t.lock()
	defer t.unlock()
	t.applyL(partial)
	t.p.touchL()
	t.p.m.saveL()
	t.publishL()
	t.p.autoStartL()
	return nil
}

// applyL sets whatever the partial names, leaving the rest alone.
func (t *Task) applyL(partial map[string]any) {
	if title, ok := partial["title"].(string); ok && strings.TrimSpace(title) != "" {
		t.info.Title = strings.TrimSpace(title)
	}
	if desc, ok := partial["description"].(string); ok && t.info.Phase == PhasePlan { // what the agent was given stays
		// Until the task is named for real — by claude on its way out of Plan,
		// or by hand — its title follows the description, so the board says what
		// it is about from the first words written.
		if t.info.Title == "" || t.info.Title == draftTitle(t.info.Description) {
			t.info.Title = draftTitle(desc)
		}
		t.info.Description = desc
	}
	if model, ok := partial["model"].(string); ok {
		t.info.Model = model
	}
	if model, ok := partial["reviewModel"].(string); ok {
		t.info.ReviewModel = model
	}
	if answer, ok := parseAnswer(partial["onReady"], AnswerHuman, AnswerReview, AnswerMerge); ok {
		t.info.OnReady = answer
	}
	if answer, ok := parseAnswer(partial["onAccept"], AnswerHuman, AnswerMerge); ok {
		t.info.OnAccept = answer
	}
	if raw, ok := partial["reviewLoops"]; ok {
		t.info.ReviewLoops = parseLoops(raw)
	}
	if raw, ok := partial["budget"]; ok {
		t.info.Budget = parseBudget(raw)
	}
	if raw, ok := partial["startAfter"].([]any); ok {
		var after []string
		seen := map[string]bool{}
		for _, d := range raw {
			s, ok := d.(string)
			if !ok || seen[s] || s == t.tid || t.p.tasks[s] == nil || t.p.startsAfterL(s, t.tid, nil) { // dropping cycles keeps waiting tasks startable
				continue
			}
			seen[s] = true
			after = append(after, s)
		}
		t.info.StartAfter = after
	}
}

// finishedL: the task is over, merged or closed without merging. Nothing
// waits for it any longer, and no agent is sent into it.
func (t *Task) finishedL() bool {
	return t.info.Phase == PhaseDone || t.info.Phase == PhaseClosed
}

// blockedL: some task this one follows is not done yet. A deleted one is
// nothing to wait for.
func (t *Task) blockedL() bool {
	for _, tid := range t.info.StartAfter {
		if dep := t.p.tasks[tid]; dep != nil && !dep.finishedL() {
			return true
		}
	}
	return false
}

// autoStartableL: a task set to follow others, whose wait is over. Until then
// it stays in Plan, so there is no half-made workspace anywhere; an open plan
// holds it back as well, that being someone still writing the description the
// agent is about to be handed.
func (t *Task) autoStartableL() bool {
	return t.info.Phase == PhasePlan && len(t.info.StartAfter) > 0 && !t.autoStarting &&
		t.viewers == 0 && strings.TrimSpace(t.info.Description) != "" && !t.blockedL()
}

// overBudgetL: the budget (if any) leaves no meaningful room for another run.
func (t *Task) overBudgetL() bool {
	return t.info.Budget != nil && *t.info.Budget-t.info.Spent < 0.01
}

func (t *Task) noteBudgetL() {
	t.note(fmt.Sprintf("the task budget ($%g) is used up (spent $%.2f); raise it in the task settings to continue", *t.info.Budget, t.info.Spent))
	t.queueL("budget", budgetPrompt(*t.info.Budget, t.info.Spent))
}

// --- phase transitions ---

// ensureTitle names a task on its way out of Plan. It has been calling itself
// after its description all along (see applyL), which will do; claude is asked
// for something better in the background, so nothing waits on the naming. That
// happens once in a task's life, and the answer is adopted only while the
// stand-in is still there — a rename meanwhile wins.
func (t *Task) ensureTitle() error {
	t.lock()
	defer t.unlock()
	desc := t.info.Description
	if strings.TrimSpace(desc) == "" {
		return errors.New("Give the task a description first")
	}
	stand := draftTitle(desc)
	if t.info.Title == "" {
		t.setTitleL(stand)
	}
	if t.info.TitleAsked {
		return nil
	}
	t.info.TitleAsked = true
	t.p.m.saveL()
	if t.info.Title != stand {
		return nil // named by hand already: leave it be
	}
	go func() {
		title := generateTitle(desc)
		t.lock()
		defer t.unlock()
		if title != "" && t.info.Title == stand && t.p.tasks[t.tid] == t { // not renamed, not deleted meanwhile
			t.setTitleL(title)
		}
	}()
	return nil
}

func (t *Task) setTitleL(title string) {
	t.info.Title = title
	t.p.m.saveL()
	t.pubL("title", title)
}

// Assign leaves the plan phase: create the workspace and hand the task over.
// A task set to follow others waits for them in Plan (see autoStartL), so its
// clone is made once their work is merged and includes it; assigning it by
// hand meanwhile is the way to start it anyway, without waiting.
func (t *Task) Assign(to string) error {
	if err := t.ensureTitle(); err != nil {
		return err
	}
	t.lock()
	t.touchL()
	desc := t.info.Description
	t.unlock()
	defer t.p.m.work()()
	if err := t.ensureWorkspace(); err != nil {
		return err
	}
	// A point before the first word is said, so that the first word can be
	// taken back like any other: rewinding to it empties the log and hands the
	// description back to the composer, the description being a message like
	// any other once the task has left Plan.
	t.mark("Start", "")
	if to == "agent" {
		e := newEntry("user")
		e.Text = desc
		t.addEntry(e)
		t.kick(desc)
	} else {
		t.lock()
		t.setPhaseL(PhaseHuman)
		t.unlock()
		t.bgUp()
	}
	return nil
}

// isCompact: the message is claude's own /compact — the user asking it to
// summarise what it remembers down to a paragraph, with or without a word on
// what to keep — rather than a message for the agent. It goes down the same
// road as one, but nothing is asked of the agent in it: the task stays whose
// it was, and nothing is read from how the turn ends (see kick, onTurnEnd).
func isCompact(text string) bool {
	return text == "/compact" || strings.HasPrefix(text, "/compact ")
}

// SendChat: a user chat message, shown in the log, then fed to (or starting)
// claude. Files attached to it are written to the task's uploads directory
// first, and the message refers to them by the path it reads them at (see
// uploads.go).
func (t *Task) SendChat(text string, files []ChatFile) error {
	text = strings.TrimSpace(text)
	if text == "" && len(files) == 0 {
		return nil
	}
	compact := isCompact(text)
	t.lock()
	switch {
	case t.info.Phase == PhasePlan:
		t.unlock()
		return errors.New("Assign the task to the agent first")
	// A message sent while claude works steers the turn under way, and one
	// sent while it compacts is queued behind that; a /compact is neither,
	// and claude sent one mid-turn drops it without a word.
	case compact && t.workingL():
		t.unlock()
		return errors.New("Claude is busy; wait for the turn to end, or stop it first")
	// The session a review runs is the reviewer's, and that one is nobody's
	// conversation (see review.go); the agent has the task back soon enough.
	case compact && t.info.Phase == PhaseReview:
		t.unlock()
		return errors.New("A review is under way; this is the agent's own memory, and it has the task back after")
	// A message is the turn a wait for the limit was for, or what replaces it
	// (see kick); a /compact is neither, and would run into the limit itself.
	case compact && t.info.LimitUntil > 0:
		t.unlock()
		return errors.New("Claude is waiting out a usage limit; the compaction would run into it too")
	// A message picks a finished task back up, into a fresh clone; that is
	// not what asking claude to forget things is for.
	case compact && !t.hasWorkspace():
		t.unlock()
		return errors.New("This task has no workspace to work from; pick it up first")
	}
	t.unlock()
	text, err := t.saveUploads(text, files)
	if err != nil {
		return err
	}
	t.lock()
	t.touchL()
	t.p.touchL()
	// A word from the user is a fresh start for the automatic review: its
	// rounds are counted per thing asked for, and the feedback that was waiting
	// here has been answered, ignored, or sent back in — either way it is said.
	// A /compact says nothing to anyone, and leaves it all standing.
	if !compact {
		t.doneNudges = 0
		t.info.ReviewLoop = 0
		t.setReviewL("")
	}
	// A review is a second opinion, and the user's own word outranks it: the
	// task is the agent's again, so the kick below swaps the reviewer out for it.
	if t.info.Phase == PhaseReview {
		t.setPhaseL(PhaseAgent)
	}
	t.unlock()
	e := newEntry("user")
	e.Text = text
	t.addEntry(e)
	t.kick(text)
	return nil
}

func sameBudget(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// kick makes sure a claude session is running and feeds it text, in the
// background. A task being merged stays in that phase: the agent then works
// on the merge. A finished task picks the conversation up where it ended, in a
// workspace freshly made from the branch (see ensureWorkspace); the notes
// waiting for the agent tell it what happened meanwhile.
func (t *Task) kick(text string) {
	t.lock()
	t.clearLimitL() // a turn now is what the wait was for, or what replaces it
	if t.overBudgetL() {
		t.noteBudgetL()
		if t.agentPhaseL() {
			t.setPhaseL(PhaseHuman)
		}
		t.unlock()
		return
	}
	pickedUp := t.finishedL()
	// A /compact asks nothing of the agent: the task is not its for the
	// asking, and the notes waiting for it wait for a message (see isCompact).
	compact := isCompact(text)
	// The agent is being sent in, rather than carrying on: whatever is in the
	// tree is not its doing, and becomes a commit of its own before it starts
	// (see mark), so the point at the end of its run holds its work alone.
	sendingIn := !compact && !t.agentPhaseL()
	// The phases that run an agent of their own keep it: a merge's turn belongs
	// to resolving conflicts, a review's to the reviewer. Anything else sent in
	// means the task is the agent's again — which is how a message of the user's
	// takes it back from a reviewer (see SendChat).
	if !compact && t.info.Phase != PhaseMerge && t.info.Phase != PhaseReview {
		t.setPhaseL(PhaseAgent)
	}
	// A running claude is the reviewer or the task's own agent, with its
	// spending cap fixed at start: either changing needs a new process.
	review := t.info.Phase == PhaseReview
	old := t.session
	restart := old != nil && (t.sessionReview != review || !sameBudget(t.sessionBudget, t.info.Budget))
	branch := t.p.defaultBranch
	t.unlock()
	go func() {
		if restart {
			t.stopSession(old)
		}
		if pickedUp {
			t.note("picked up again: the workspace is a fresh clone of " + branch)
		}
		// The workspace is made before the container comes up on it. Anywhere
		// but out of a finished task there is one already, and this is a
		// no-op; it is what makes one that is somehow missing (or not a clone,
		// see hasWorkspace) come back, instead of every git command the agent
		// runs failing in it.
		if err := t.ensureWorkspace(); err != nil {
			t.failKick("workspace failed", err)
			return
		}
		if sendingIn {
			t.mark("Human", "")
		}
		s, err := t.ensureSession()
		if err != nil {
			t.failKick("agent start failed", err)
			return
		}
		// Taken only now that there is someone to tell: a kick that never got
		// this far leaves the notes waiting for the next one. What happened to
		// the workspace is the agent's to hear and none of the reviewer's
		// business, so a review leaves the notes where they are.
		t.lock()
		if !review && !compact {
			text = t.takePendingL(text)
		}
		t.unlock()
		s.Send(text)
		t.lock()
		t.publishL()
		t.unlock()
	}()
}

// failKick reports why the agent never got its message, and leaves the task
// with the human it came from.
func (t *Task) failKick(prefix string, err error) {
	t.noteErr(prefix, err)
	t.lock()
	defer t.unlock()
	if t.agentPhaseL() {
		t.setPhaseL(PhaseHuman)
	}
}

func (t *Task) stopSession(s *ChatSession) {
	t.lock()
	t.stopping = true
	t.unlock()
	s.Stop()
	t.lock()
	t.stopping = false
	t.unlock()
}

// StopAgent is the user stopping the agent: the task goes to them, and what it
// had got to becomes a save point to come back to.
func (t *Task) StopAgent() error {
	if t.stopAgent() {
		t.mark("Agent", "stopped part-way")
	}
	return nil
}

// probeContextParts learns what claude's window holds before a word is said in
// it, and where claude will compact — the two things the dashboard's gauge
// needs and no turn reports (see context.go). Neither changes while the model
// does not, so a window already measured for it is left alone; the asking is
// done in the background, a task waiting on it waiting on nothing it needs.
func (t *Task) probeContextParts(c *Container) {
	t.lock()
	model := t.info.Model
	// The reviewer's window is its own, and starts empty every time (see
	// review.go): what it holds says nothing about the task's conversation.
	if t.sessionReview || (t.info.Window != nil && t.info.Window.Model == model) {
		t.unlock()
		return
	}
	t.unlock()
	p, ok := probeContext(c, model, systemPrompt)
	if !ok {
		return
	}
	p.Model = model // in the task's own words, which is what the guard above reads
	t.lock()
	t.info.Window = &p
	t.p.m.saveL()
	t.publishL()
	t.unlock()
}

// stopAgent hands the task to the human, interrupting any agent turn (or
// merge), and reports whether it cut a turn short. No save point: the callers
// here are clearing the way for something else — a merge, a close, a revert —
// whose own work is what comes next.
func (t *Task) stopAgent() bool {
	t.lock()
	s := t.session
	// A turn running while the task is not the agent's is a /compact of the
	// user's (see isCompact): stopping that leaves nothing half done for the
	// agent to be told about, and nothing worth a save point.
	cutOff := t.workingL() && t.agentPhaseL()
	if cutOff {
		// Its turn is about to be cut off mid-thought; whatever it was doing
		// was left half done, which it has no other way of finding out.
		t.queueL("stopped", stoppedPrompt)
	}
	t.unlock()
	if s != nil {
		t.stopSession(s)
	}
	t.lock()
	if t.agentPhaseL() {
		t.setPhaseL(PhaseHuman)
	}
	t.unlock()
	return cutOff
}

// MoveTo maps board drags onto the real actions. Dropped on Done, a task is
// merged; Closed is where it goes when it should not be (see Close).
func (t *Task) MoveTo(phase Phase) error {
	t.lock()
	current := t.info.Phase
	t.unlock()
	valid := false
	for _, p := range phases {
		valid = valid || p == phase
	}
	if !valid || phase == current {
		return nil
	}
	switch phase {
	case PhasePlan:
		return t.Discard()
	case PhaseAgent:
		if current == PhasePlan {
			return t.Assign("agent")
		}
		return t.SendChat("Please continue working on the task.", nil)
	case PhaseReview:
		return t.StartReview()
	case PhaseHuman, PhaseMuted:
		// Both wait for a human; muting is that with the task put away. Getting
		// there is the same work either way, and only the phase set at the end
		// differs — a task coming from Plan or a closed one has a workspace
		// made first.
		switch current {
		case PhasePlan:
			if err := t.Assign("human"); err != nil {
				return err
			}
		case PhaseDone, PhaseClosed:
			if err := t.reopen(); err != nil {
				return err
			}
		case PhaseAgent, PhaseReview, PhaseMerge:
			if err := t.StopAgent(); err != nil {
				return err
			}
		}
		t.lock()
		defer t.unlock()
		if t.info.Phase != phase {
			t.setPhaseL(phase)
		}
		return nil
	case PhaseClosed:
		return t.Close()
	default:
		return t.Merge("")
	}
}

// Close puts a task away without merging it: its work is kept as a patch,
// off the default branch (see park), and comes back when the task is picked
// up again — or is thrown out with the task. The dashboard offers this
// beside merging, and recommends merging.
func (t *Task) Close() error {
	defer t.p.m.work()()
	t.stopAgent()
	t.lock()
	t.info.CommitMessage = ""
	t.setPhaseL(PhaseClosed)
	t.unlock()
	t.park()
	return nil
}

// Merge replays the workspace's work onto the latest default branch (see
// replant), commits it as one commit, and fast-forwards the project repo. The
// task sits in the merge phase meanwhile: conflicts are handed to the task's
// agent there — it knows what its side of them is for — whose 'merge' verdict
// runs it again. A failure puts the task
// back with the human.
func (t *Task) Merge(message string) error {
	defer t.p.m.work()()
	repo := t.repoDir()
	if !t.hasWorkspace() {
		return errors.New("The task has no workspace to merge: it is still in Plan, or finished already")
	}
	t.stopAgent()
	t.lock()
	message = strings.TrimSpace(message)
	if message == "" {
		message = strings.TrimSpace(t.info.CommitMessage)
	}
	if message == "" {
		message = strings.TrimSpace(t.info.Title)
	}
	t.info.CommitMessage = message
	t.setPhaseL(PhaseMerge)
	t.unlock()
	err := t.merge(repo, message)
	if err != nil {
		t.noteErr("merge failed", err)
		t.lock()
		if t.info.Phase == PhaseMerge {
			t.setPhaseL(PhaseHuman)
		}
		t.unlock()
	}
	go t.refreshChanges()
	return err
}

func (t *Task) merge(repo, message string) error {
	branch := t.p.defaultBranch
	conflicts, err := t.replant(repo)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		t.note(fmt.Sprintf("merging onto the latest %s hit conflicts in %s; sending the agent in to resolve them", branch, strings.Join(conflicts, ", ")))
		t.lock()
		t.dropPendingL("conflicts") // the prompt says it
		prompt := conflictPrompt(branch, t.info.CommitMessage, conflicts)
		t.unlock()
		t.kick(prompt) // it comes back through onTurnEnd
		return nil
	}
	// Whatever a replay left in the files — or an agent that reported 'merge'
	// without resolving — must not land on the branch.
	if marked, err := t.markedFiles(repo); err != nil {
		return err
	} else if len(marked) > 0 {
		return fmt.Errorf("conflict markers are left in %s", strings.Join(marked, ", "))
	}
	if _, err := git(repo, "add", "-A"); err != nil {
		return err
	}
	if !gitOK(repo, "diff", "--cached", "--quiet") {
		if _, err := git(repo, "commit", "--no-verify", "-m", message); err != nil {
			return err
		}
	}
	target, _ := git(repo, "rev-parse", "origin/"+branch) // what replant fetched
	if head, _ := git(repo, "rev-parse", "HEAD"); head != target {
		if err := t.p.fastForward(repo); err != nil {
			return err
		}
		t.note("merged into " + branch + " ✔")
	} else {
		t.note("nothing to merge; task closed")
	}
	// Before the task is Done, so it never sits there with a workspace someone
	// could still open or merge a second time.
	if err := t.dropWorkspace(); err != nil {
		logf("%s: dropping the workspace: %v", t.key(), err)
	}
	t.lock()
	t.info.CommitMessage = ""
	t.info.Conflicts = nil
	t.info.ReviewLoop = 0
	t.setReviewL("")
	// The workspace these were about is gone; a task picked up after the merge
	// gets a fresh clone, and hears about that instead.
	for _, key := range []string{"rebase", "conflicts", "reopened", "container", "stopped", "restart"} {
		t.dropPendingL(key)
	}
	t.queueL("merged", mergedPrompt(branch))
	t.setPhaseL(PhaseDone)
	t.unlock()
	t.park()
	return nil
}

// Rebase brings the workspace onto the latest default branch without merging
// anything into it: the replay a merge starts with (see replant), and nothing
// after it. Conflicts are left in the files for whoever comes next — the user
// in VS Code, or the agent, which is told what happened to its workspace the
// next time it is sent in (see queueL).
func (t *Task) Rebase() error {
	defer t.p.m.work()()
	repo := t.repoDir()
	if !t.hasWorkspace() {
		return errors.New("The task has no workspace to rebase")
	}
	_ = t.StopAgent()
	branch := t.p.defaultBranch
	conflicts, err := t.replant(repo)
	if err != nil {
		t.noteErr("rebase failed", err)
		return err
	}
	if len(conflicts) > 0 {
		t.note(fmt.Sprintf("rebased onto the latest %s; conflicts in %s are left to resolve", branch, strings.Join(conflicts, ", ")))
	} else {
		t.note("rebased onto the latest " + branch + " ✔")
	}
	t.queue("rebase", replantedPrompt(branch))
	go t.refreshChanges()
	return nil
}

// --- the work as a patch ---
//
// A task's work is what its tree differs from the branch in: from the commit
// it was cloned at, uncommitted changes and untracked files included. Taken
// off as a patch, that can be put back on top of any later commit with git's
// merge machinery — by making a commit of it on its base, and cherry-picking
// that without committing — which is how a workspace is brought up to date
// (replant) and how a closed task, which keeps nothing but the patch, gets a
// workspace back (ensureWorkspace). No git operation stays in progress
// afterwards: conflicts are markers in the files, and the work is uncommitted
// again, whatever the outcome.

// workSubject is the throwaway commit's message; the conflict markers name it
// (see markedFiles).
const workSubject = "TPS task work"

// tmpIndex is a place for a throwaway index, so the real one stays untouched;
// the environment makes git use it (and make it: git wants none or a whole one).
func (t *Task) tmpIndex() (env []string, cleanup func(), err error) {
	// Outside the task's own directory: a changes overview runs in the
	// background (see refreshChanges) and can still be writing this while the
	// task is being parked or thrown away, which would put a file back in a
	// directory something else is busy emptying. Git wants no particular
	// place for an index it is handed.
	f, err := os.CreateTemp("", "tps-index-*")
	if err != nil {
		return nil, nil, err
	}
	f.Close()
	os.Remove(f.Name())
	return []string{"GIT_INDEX_FILE=" + f.Name()}, func() { os.Remove(f.Name()) }, nil
}

// stageAll stages the whole tree in a copy of the index (unchanged files then
// need no rehashing): the tree's every difference from a commit, untracked
// files included, is a `diff --cached` away.
func (t *Task) stageAll(repo string) (env []string, cleanup func(), err error) {
	env, cleanup, err = t.tmpIndex()
	if err != nil {
		return nil, nil, err
	}
	if data, err := os.ReadFile(filepath.Join(repo, ".git", "index")); err == nil {
		_ = os.WriteFile(strings.TrimPrefix(env[0], "GIT_INDEX_FILE="), data, 0o644)
	}
	if _, err := runCmd([]string{"git", "-C", repo, "add", "-A"}, RunOpts{Env: env}); err != nil {
		cleanup()
		return nil, nil, err
	}
	return env, cleanup, nil
}

// savePatch writes the tree's work since base to the patch file, headed by the
// commit it is against (git skips the line). No work, no patch.
func (t *Task) savePatch(repo, base string) error {
	env, cleanup, err := t.stageAll(repo)
	if err != nil {
		return err
	}
	defer cleanup()
	r, err := runCmd([]string{"git", "-C", repo, "diff", "--cached", "--binary", "--full-index", "--no-renames", base}, RunOpts{Env: env})
	if err != nil {
		return err
	}
	if r.Out == "" {
		os.Remove(t.patchFile())
		return nil
	}
	return os.WriteFile(t.patchFile(), []byte("base "+base+"\n\n"+r.Out), 0o644)
}

// workCommit makes a commit of the patch's work on top of its base, in the
// clone's object store — the patch applied to the base's tree in an index of
// its own, never the working tree — for plant to replay.
func (t *Task) workCommit(repo string) (string, error) {
	head, _, _ := strings.Cut(readFile(t.patchFile()), "\n")
	base, ok := strings.CutPrefix(head, "base ")
	if !ok {
		return "", errors.New("the patch names no base commit")
	}
	env, cleanup, err := t.tmpIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	for _, args := range [][]string{{"read-tree", base}, {"apply", "--cached", "--binary", t.patchFile()}} {
		if _, err := runCmd(append([]string{"git", "-C", repo}, args...), RunOpts{Env: env}); err != nil {
			return "", err
		}
	}
	r, err := runCmd([]string{"git", "-C", repo, "write-tree"}, RunOpts{Env: env})
	if err != nil {
		return "", err
	}
	return git(repo, "commit-tree", strings.TrimSpace(r.Out), "-p", base, "-m", workSubject)
}

// plant replays the work commit onto the working tree, which is clean at the
// branch's tip, and leaves it uncommitted work again: nothing staged, no
// cherry-pick in progress. Returned are the files that did not merge cleanly:
// left with conflict markers, or — deleted on the branch and changed by the
// task — as the task had them.
func plant(repo, work string) ([]string, error) {
	var conflicts []string
	if _, err := git(repo, "cherry-pick", "--no-commit", work); err != nil {
		if out, _ := git(repo, "diff", "--name-only", "-z", "--diff-filter=U"); out != "" {
			conflicts = strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
		}
		if len(conflicts) == 0 {
			return nil, err
		}
		if _, err := git(repo, "cherry-pick", "--quit"); err != nil {
			return nil, err
		}
	}
	if _, err := git(repo, "reset", "--quiet"); err != nil {
		return nil, err
	}
	return conflicts, nil
}

// replant brings the workspace onto the latest default branch: its work comes
// off as a patch, the tree is reset to the branch's tip, and the work goes
// back on top (see plant), what did not merge cleanly being remembered in
// Conflicts. Commits the agent made despite the rules are folded into the
// work. Shared by the merge, which commits afterwards, and by Rebase, which
// stops here.
func (t *Task) replant(repo string) ([]string, error) {
	branch := t.p.defaultBranch
	if _, err := git(repo, "fetch", "--quiet", "origin"); err != nil {
		return nil, err
	}
	target, err := git(repo, "rev-parse", "origin/"+branch)
	if err != nil {
		return nil, err
	}
	if head, _ := git(repo, "rev-parse", "HEAD"); head == target {
		return nil, nil // the branch has nothing this workspace lacks
	}
	base, err := git(repo, "merge-base", "HEAD", target)
	if err != nil {
		return nil, err
	}
	if err := t.savePatch(repo, base); err != nil {
		return nil, err
	}
	if _, err := git(repo, "reset", "--quiet", "--hard", target); err != nil {
		return nil, err
	}
	if _, err := git(repo, "clean", "--quiet", "-fd"); err != nil { // the patch has the untracked files; ignored ones stay
		return nil, err
	}
	return t.plantPatch(repo)
}

// plantPatch puts the patch file's work onto the workspace and removes it: a
// patch beside a workspace is work the workspace lacks (see ensureWorkspace).
func (t *Task) plantPatch(repo string) ([]string, error) {
	if !exists(t.patchFile()) {
		return nil, nil
	}
	work, err := t.workCommit(repo)
	if err != nil {
		return nil, err
	}
	conflicts, err := plant(repo, work)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(t.patchFile()); err != nil {
		return nil, err
	}
	t.lock()
	defer t.unlock()
	t.setConflictsL(conflicts)
	return conflicts, nil
}

// setConflictsL records what the last replay could not merge, for the
// dashboard and — as a note for its next turn — the agent.
func (t *Task) setConflictsL(files []string) {
	t.info.Conflicts = files
	if len(files) > 0 {
		t.queueL("conflicts", conflictsPrompt(files))
	} else {
		t.dropPendingL("conflicts")
	}
	t.p.m.saveL()
	t.publishL()
}

// markerRe matches the closing marker plant leaves: git names the commit in
// it, and the commit is ours.
var markerRe = `^>>>>>>> [0-9a-f]+ \(` + workSubject + `\)`

// markedFiles are the files in the workspace that still hold conflict markers
// from a replay: what a merge stops on.
func (t *Task) markedFiles(repo string) ([]string, error) {
	base, err := git(repo, "merge-base", "HEAD", "origin/"+t.p.defaultBranch)
	if err != nil {
		return nil, err
	}
	env, cleanup, err := t.stageAll(repo)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	r, err := runCmd([]string{"git", "-C", repo, "diff", "--cached", "--name-only", "-z", "-E", "-G" + markerRe, base}, RunOpts{Env: env})
	if err != nil || r.Out == "" {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(r.Out, "\x00"), "\x00"), nil
}

// --- finished tasks: parked, and picked up again ---

// archived is what a finished task keeps besides its patch, compressed into
// one archive: claude's state, the chat log, the files the user attached, and
// the services' last output.
var archived = []string{"claude", "chat.jsonl", "uploads", "services"}

// park puts a finished task's data away, so that a task nobody looks at costs
// as little disk as it can: the workspace goes — with its work kept as a
// patch if the task was closed without merging; a merged one's is on the
// branch — and what the task keeps is compressed (see archived). Picking the
// task up undoes it (see ensureWorkspace). Done when a task finishes, and at
// startup for those an older TPS left as they were.
func (t *Task) park() {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	t.lock()
	finished, closed := t.finishedL(), t.info.Phase == PhaseClosed
	t.unlock()
	if !finished {
		return
	}
	if t.hasWorkspace() {
		if closed {
			repo := t.repoDir()
			_, err := git(repo, "fetch", "--quiet", "origin")
			var base string
			if err == nil {
				base, err = git(repo, "merge-base", "HEAD", "origin/"+t.p.defaultBranch)
			}
			if err == nil {
				err = t.savePatch(repo, base)
			}
			if err != nil {
				t.noteErr("keeping the work as a patch failed", err) // the workspace stays: it is all there is of the work
				return
			}
			if exists(t.patchFile()) {
				t.note(fmt.Sprintf("closed without merging: the work is kept as a patch, off %[1]s; picking the task up applies it onto the latest %[1]s", t.p.defaultBranch))
			} else {
				t.note("closed without merging; there was no work to keep")
			}
		}
		if err := t.dropWorkspace(); err != nil {
			logf("%s: dropping the workspace: %v", t.key(), err)
		}
	}
	if err := t.archive(); err != nil {
		logf("%s: compressing the task's data: %v", t.key(), err)
	}
	go t.refreshChanges() // the patch, from here on
}

// archive compresses what a finished task keeps (see archived) into one
// tarball and removes the originals; nothing to do when that is done already.
// A note arriving meanwhile waits, and goes to a chat log beside the archive.
func (t *Task) archive() error {
	if exists(t.archiveFile()) {
		return nil
	}
	t.chatMu.Lock()
	defer t.chatMu.Unlock()
	var names []string
	for _, name := range archived {
		if exists(filepath.Join(t.dir(), name)) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	tmp := t.archiveFile() + ".tmp"
	if _, err := runCmd(append([]string{"tar", "-C", t.dir(), "-czf", tmp}, names...), RunOpts{}); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, t.archiveFile()); err != nil {
		return err
	}
	for _, name := range names {
		if err := rmTree(filepath.Join(t.dir(), name)); err != nil {
			return err
		}
	}
	return nil
}

// unarchive is archive undone. A note made meanwhile went to a chat log of
// its own beside the archive (see appendChat); it follows the archived one.
func (t *Task) unarchive() error {
	if !exists(t.archiveFile()) {
		return nil
	}
	t.chatMu.Lock()
	defer t.chatMu.Unlock()
	since, _ := os.ReadFile(t.chatFile())
	if _, err := runCmd([]string{"tar", "-C", t.dir(), "-xzf", t.archiveFile()}, RunOpts{}); err != nil {
		return err
	}
	if len(since) > 0 {
		f, err := os.OpenFile(t.chatFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = f.Write(since)
		f.Close()
		if err != nil {
			return err
		}
	}
	return os.Remove(t.archiveFile())
}

// ensureWorkspace gives the task a workspace with its work in it, if it has
// none: a finished task's data comes out of its archive, the branch is cloned
// afresh, and the patch a closed task kept goes on top — onto the branch as it
// is now, so the work meets what landed since (see plant). A task that has a
// workspace already is left alone, except for a patch waiting beside it (a
// replay the daemon died in the middle of), which is planted.
func (t *Task) ensureWorkspace() error {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	if err := t.unarchive(); err != nil {
		return err
	}
	if err := t.clone(); err != nil {
		return err
	}
	if !exists(t.patchFile()) {
		return nil
	}
	branch := t.p.defaultBranch
	conflicts, err := t.plantPatch(t.repoDir())
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		t.note(fmt.Sprintf("the task's work was applied onto the latest %s; conflicts in %s are left to resolve", branch, strings.Join(conflicts, ", ")))
	} else {
		t.note("the task's work was applied onto the latest " + branch + " ✔")
	}
	t.queue("reopened", reopenedPrompt(branch))
	return nil
}

// dropWorkspace throws the clone away, container first: what the task knows
// stays — claude's own state and the chat log live beside the clone, not in
// it — and picking the task up again clones the branch afresh, the way
// leaving Plan does.
func (t *Task) dropWorkspace() error {
	t.down()
	if err := rmTree(t.repoDir()); err != nil {
		// The clone is out of the way regardless, so the task goes on: what could
		// not be unlinked costs disk, which only the human can free.
		t.note("workspace not fully removed: " + err.Error())
	}
	t.lock()
	defer t.unlock()
	t.pubL("changes", nil)
	return nil
}

// reopen picks a finished task back up, for a human to work in: a workspace
// from the branch as it is now — a closed task's work put back on top of it
// (see ensureWorkspace) — with the conversation it kept. The agent gets the
// same through kick.
func (t *Task) reopen() error {
	defer t.p.m.work()()
	t.lock()
	from := t.info.Phase
	t.setPhaseL(PhaseHuman) // first, so that park (which runs at startup too) leaves the task alone
	t.unlock()
	t.note("picked up again: the workspace is a fresh clone of " + t.p.defaultBranch)
	if err := t.ensureWorkspace(); err != nil {
		t.noteErr("workspace failed", err)
		t.lock()
		t.setPhaseL(from)
		t.unlock()
		return err
	}
	t.bgUp()
	go t.refreshChanges()
	return nil
}

// containerfile is what the task's container is built from: the repository's
// own Containerfile.dev, or the default image when it has none.
func (t *Task) containerfile() string {
	if cf := readFile(filepath.Join(t.repoDir(), containerfile)); cf != "" {
		return cf
	}
	return defaultContainerfile
}

// adopt picks up a container still running from before a daemon restart, if
// it matches what would be started now.
func (t *Task) adoptL() {
	if t.info.Phase == PhasePlan || !t.hasWorkspace() {
		return
	}
	cf := t.containerfile()
	tag := imageTag(cf)
	services, caches := containerfileDeclarations(cf)
	if c := runningContainer(t.containerName(), containerConfig(tag, toolboxDir(), caches)); c != nil {
		if _, err := nestFor(t.containerName(), nestDir(t.dir())); err != nil {
			logf("%s: docker socket: %v", t.key(), err)
		}
		t.container, t.lastTag = c, tag
		t.setDeclaredL(services)
		t.status = StatusUp
		t.syncServicesL(false) // what was left running rides on
		// A code-server still running in it is kept: the dashboards that held
		// it are about to reconnect. One nobody comes back for goes in the sweep.
		if codeAlive(c.CodePort) {
			t.codeUp, t.codeStart = true, time.Now().UnixMilli()
		}
	}
}

// Discard: back to plan, all work is thrown away (the UI asks for confirmation).
func (t *Task) Discard() error {
	defer t.p.m.work()()
	t.stopAgent()
	t.down()
	nestPurge(t.containerName(), nestDir(t.dir()))
	_ = rmContainer(t.containerName()) // also one the daemon never knew about
	if err := rmTree(t.dir()); err != nil {
		logf("%s: discarding the workspace: %v", t.key(), err)
	}
	t.lock()
	defer t.unlock()
	t.info.Started = false
	t.info.CommitMessage = ""
	t.info.ReviewLoop = 0
	t.setReviewL("")
	t.info.Conflicts, t.info.Pending = nil, nil // nothing of the old workspace is left to tell
	t.p.m.hub.SetChat(t.key(), nil)
	t.pubL("changes", nil)
	t.setPhaseL(PhasePlan)
	return nil
}

func (t *Task) Delete() error {
	defer t.p.m.work()()
	t.stopAgent()
	t.down()
	nestPurge(t.containerName(), nestDir(t.dir()))
	_ = rmContainer(t.containerName())
	if err := rmTree(t.dir()); err != nil {
		logf("%s: deleting the workspace: %v", t.key(), err)
	}
	t.lock()
	defer t.unlock()
	delete(t.p.info.Tasks, t.tid)
	delete(t.p.tasks, t.tid)
	t.p.m.hub.Set([]string{"projects", t.p.pid, "tasks", t.tid}, nil)
	t.p.m.hub.SetChat(t.key(), nil)
	t.p.touchL()
	t.p.m.saveL()
	t.p.autoStartL() // a deleted task is no longer something to wait for
	return nil
}

// --- the changed files overview ---

type change struct {
	Path string `json:"p"`
	Add  int    `json:"a"`
	Del  int    `json:"d"`
	Bin  bool   `json:"bin,omitempty"`
}

// changes lists what the working tree changed since the commit the task
// started from, untracked files included (see stageAll) — or, for a closed
// task, what its patch holds.
func (t *Task) changes() ([]change, error) {
	repo := t.repoDir()
	if !t.hasWorkspace() {
		if !exists(t.patchFile()) {
			return nil, nil
		}
		r, err := runCmd([]string{"git", "apply", "--numstat", "-z", t.patchFile()}, RunOpts{Dir: t.p.dir()})
		if err != nil {
			return nil, err
		}
		return parseNumstat(r.Out), nil
	}
	base, err := git(repo, "merge-base", "HEAD", "origin/"+t.p.defaultBranch)
	if err != nil {
		return nil, err
	}
	env, cleanup, err := t.stageAll(repo)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	r, err := runCmd([]string{"git", "-C", repo, "diff", "--cached", "--numstat", "--no-renames", "-z", base}, RunOpts{Env: env})
	if err != nil {
		return nil, err
	}
	return parseNumstat(r.Out), nil
}

// parseNumstat reads `git diff --numstat -z`: "added\tdeleted\tpath\0" per file, "-" counts for binaries.
func parseNumstat(out string) []change {
	changes := []change{}
	for _, line := range strings.Split(out, "\x00") {
		add, rest, ok := strings.Cut(line, "\t")
		del, path, ok2 := strings.Cut(rest, "\t")
		if !ok || !ok2 {
			continue
		}
		c := change{Path: path, Bin: add == "-"}
		if !c.Bin {
			c.Add, _ = strconv.Atoi(add)
			c.Del, _ = strconv.Atoi(del)
		}
		changes = append(changes, c)
	}
	return changes
}

// refreshChanges publishes the changes overview, unless one is being made already.
func (t *Task) refreshChanges() {
	t.lock()
	if t.refreshing {
		t.unlock()
		return
	}
	t.refreshing = true
	t.unlock()
	changes, err := t.changes()
	t.lock()
	defer t.unlock()
	t.refreshing = false
	if err != nil {
		logf("%s: changes: %v", t.key(), err)
		return
	}
	if changes == nil {
		t.pubL("changes", nil)
	} else {
		t.pubL("changes", changes)
	}
	go t.refreshBehind()
}

// refreshBehind counts the commits the default branch has that this workspace
// was never given: what a rebase (see Rebase) would bring in. Where the two
// parted ways is the workspace's own business — a merge base, which holds even
// after a fetch brought newer commits into the clone without replaying onto
// them — and the project repo counts the distance from there to its branch.
func (t *Task) refreshBehind() {
	repo := t.repoDir()
	behind := 0
	if t.hasWorkspace() {
		branch := t.p.defaultBranch
		if base, err := git(repo, "merge-base", "HEAD", "origin/"+branch); err == nil {
			if out, err := git(t.p.dir(), "rev-list", "--count", base+".."+branch); err == nil {
				behind, _ = strconv.Atoi(strings.TrimSpace(out))
			}
		}
	}
	t.lock()
	defer t.unlock()
	if t.behind == behind {
		return
	}
	t.behind = behind
	if behind > 0 {
		t.pubL("behind", behind)
	} else {
		t.pubL("behind", nil)
	}
}

// --- workspace (clone + container) ---

// clone makes the workspace, unless it is there already. The caller holds cloneMu.
func (t *Task) clone() error {
	if t.hasWorkspace() {
		return nil
	}
	// Something is in its place that is not a clone (see hasWorkspace): it is
	// no workspace to anyone, and the path has to be free for git.
	if exists(t.repoDir()) {
		t.note("the workspace was not a git clone; making a fresh one")
		if err := rmTree(t.repoDir()); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(t.claudeDir(), 0o755); err != nil {
		return err
	}
	// A local git clone hardlinks the object store (objects are immutable,
	// so sharing them is safe): nearly free even for big repos. It is made
	// beside its place and moved there once complete, so that the directory
	// existing means a workspace ready to use: git makes the directory first
	// and checks the files out after, and a container brought up in between
	// (the task's page being opened is enough) would find no Containerfile.dev
	// and run the default image.
	tmp := t.repoDir() + ".tmp"
	os.RemoveAll(tmp)
	if _, err := runCmd([]string{"git", "clone", "--quiet", "-b", t.p.defaultBranch, t.p.dir(), tmp}, RunOpts{}); err != nil {
		return err
	}
	if err := t.setIdentity(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, t.repoDir())
}

// setIdentity gives a clone the identity its commits are made under: TPS's own
// ones, and whatever is committed by hand in its container.
func (t *Task) setIdentity(repo string) error {
	id := t.p.m.gitIdentity()
	if _, err := git(repo, "config", "user.name", id.Name); err != nil {
		return err
	}
	_, err := git(repo, "config", "user.email", id.Email)
	return err
}

// Open is a dashboard's retry after a workspace failure: bring the workspace
// up, if there is one — a task in Plan has none yet, a finished one no longer —
// and VS Code with it, if the dashboard holds the task.
func (t *Task) Open() {
	t.lock()
	t.touchL()
	phase := t.info.Phase
	t.unlock()
	if phase != PhasePlan && t.hasWorkspace() {
		t.bgUp()
		go t.syncCode()
	}
}

// --- VS Code: up while a dashboard holds the task open, down as soon as none does ---

// setCodeWantedL records whether anyone holds the task open (its watchers, at
// the hub), and sets the code-server after it.
func (t *Task) setCodeWantedL(wanted bool) {
	t.codeWanted = wanted
	go t.syncCode()
}

// syncCode brings code-server in line with codeWanted: started in the
// container (which is brought up first) while the task is held, stopped when
// it is let go of — by a Close, or by the dashboard's connection going. It
// converges in a loop, as the want may flip while it works; a start that fails
// is reported once, and tried again by whatever next asks.
func (t *Task) syncCode() {
	t.lock()
	if t.codeSyncing {
		t.unlock()
		return
	}
	t.codeSyncing = true
	t.unlock()
	defer func() {
		t.lock()
		t.codeSyncing = false
		t.unlock()
	}()
	for {
		t.lock()
		wanted, up, phase := t.codeWanted, t.codeUp, t.info.Phase
		t.unlock()
		if wanted == up {
			return
		}
		if wanted {
			// Nothing to show yet, or no longer: a plan has no workspace, a finished
			// task lost its one. The hold is left standing for when it has one.
			if phase == PhasePlan || !t.hasWorkspace() {
				return
			}
			if err := t.startCode(); err != nil {
				t.lock()
				t.codeError = err.Error()
				t.publishL()
				t.unlock()
				return
			}
		} else {
			t.stopCode()
		}
	}
}

func (t *Task) startCode() error {
	c, err := t.up()
	if err != nil {
		return err
	}
	if err := c.StartCode(); err != nil {
		return err
	}
	t.lock()
	defer t.unlock()
	if t.container != c { // recycled meanwhile: the loop looks again
		return nil
	}
	t.codeUp, t.codeStart, t.codeError = true, time.Now().UnixMilli(), ""
	t.touchL()
	t.publishL()
	return nil
}

func (t *Task) stopCode() {
	t.lock()
	c := t.container
	t.unlock()
	if c != nil {
		c.StopCode()
	}
	t.lock()
	defer t.unlock()
	t.codeUp = false
	t.publishL()
}

func (t *Task) bgUp() {
	go func() {
		if _, err := t.up(); err != nil {
			t.noteErr("workspace failed", err)
		}
	}()
}

func (t *Task) up() (*Container, error) {
	t.lock()
	f := t.upFlight
	if f == nil {
		f = &flight[*Container]{done: make(chan struct{})}
		t.upFlight = f
		go func() {
			done := t.p.m.work()
			f.val, f.err = t.doUp()
			done()
			t.lock()
			t.upFlight = nil
			t.unlock()
			close(f.done)
		}()
	}
	t.unlock()
	<-f.done
	return f.val, f.err
}

func (t *Task) failUp(err error) (*Container, error) {
	t.lock()
	t.setStatusL(StatusError, err.Error()) // callers report the error
	t.unlock()
	return nil, err
}

func (t *Task) doUp() (*Container, error) {
	if !t.hasWorkspace() {
		return nil, errors.New("The task has no workspace: it is still in Plan, or finished already")
	}
	cf := t.containerfile()
	tag := imageTag(cf)
	t.lock()
	// Don't recycle a container out from under a live claude session or service.
	if t.container != nil && t.status == StatusUp && (t.lastTag == tag || t.session != nil || t.anyRunningL()) {
		c := t.container
		t.unlock()
		return c, nil
	}
	t.setStatusL(StatusBuilding, "building container image")
	t.unlock()
	if !toolboxInstalled() {
		t.note("downloading code-server and claude for this host (once per TPS version)…")
	}
	toolbox, err := ensureToolbox()
	if err != nil {
		return t.failUp(err)
	}
	go t.p.m.refreshModels() // now that this host has a claude to ask
	c, err := t.start(cf, toolbox)
	imageErr := ""
	if isBuildErr(err) && cf != defaultContainerfile {
		// A broken Containerfile.dev (say, with conflict markers) must not lock
		// the user and the agent out of the task: the default image lets them
		// in to fix it. The tag stays that of the broken file, so the fallback
		// is kept until the file changes. Only a failed build falls back: what
		// goes wrong after it — podman refusing to run the container, a port it
		// cannot publish — is the host's, and another image would fail the same
		// way, so the agent is not sent to fix a file that is fine.
		t.note("building the image from Containerfile.dev failed; using the default image until it is fixed", err.Error())
		imageErr = err.Error()
		cf = defaultContainerfile
		c, err = t.start(cf, toolbox)
	}
	if err != nil {
		return t.failUp(err)
	}
	// The workspace can go while the container comes up — a merge dropping it,
	// say, which takes the container down first and so misses this one. Podman
	// made the mount point back when it started; both go, and the caller hears
	// what it would have heard a moment earlier.
	if !t.hasWorkspace() {
		_ = rmWorkspace(t.containerName(), nestDir(t.dir()))
		_ = os.Remove(t.repoDir()) // only if it is the empty mount point
		t.lock()
		t.container = nil
		t.setStatusL(StatusDown, "")
		t.unlock()
		return nil, errors.New("The task has no workspace: it is still in Plan, or finished already")
	}
	t.lock()
	t.container, t.lastTag = c, tag
	t.setDeclaredL(containerfileServices(cf))
	t.codeUp = false // a fresh container has no code-server in it yet
	t.setStatusL(StatusUp, "")
	t.syncServicesL(t.viewers > 0)
	if imageErr != "" {
		t.queueL("image", fallbackPrompt(imageErr))
		t.fixImageL()
	} else {
		t.dropPendingL("image") // the file builds again: nothing left to tell
	}
	t.unlock()
	go t.syncCode() // VS Code back up in it, if the task is held
	return c, nil
}

// fixImageL sends the agent in to repair Containerfile.dev, but only when the
// task is nobody's: a phase with an agent in it — its own, the reviewer, a
// merge — has a claude on this workspace already, and a second one is what must
// never happen. Those hear of it anyway, the note waiting for the turn that
// comes next (see queueL); a kick underway carries it in itself.
func (t *Task) fixImageL() {
	if t.agentPhaseL() || t.sessionFlight != nil || t.finishedL() {
		return
	}
	go func() {
		t.note("sending in the agent to fix Containerfile.dev")
		t.kick(fixImagePrompt)
	}()
}

// buildErr marks a failure of the image build itself: the Containerfile's own
// doing, and the only failure the default image can stand in for (see doUp).
type buildErr struct{ error }

func isBuildErr(err error) bool {
	var be buildErr
	return err != nil && errors.As(err, &be)
}

// start builds the image for this Containerfile (if needed) and starts the task's container from it.
func (t *Task) start(cf, toolbox string) (*Container, error) {
	tag := imageTag(cf)
	if !imageExists(tag) {
		t.note("building the dev container image; the first build takes a few minutes…")
	}
	if err := buildImage(tag, cf, t.repoDir(), func(string) {}); err != nil {
		return nil, buildErr{err}
	}
	t.lock()
	t.setStatusL(StatusStarting, "starting container")
	t.unlock()
	_, caches := containerfileDeclarations(cf)
	return ensureContainer(containerOpts{name: t.containerName(), image: tag, toolbox: toolbox, repoDir: t.repoDir(), claudeDir: t.claudeDir(), servicesDir: t.servicesDir(), uploadsDir: t.uploadsDir(), nestDir: nestDir(t.dir()), cacheDir: t.p.cacheDir(), caches: caches})
}

func (t *Task) down() {
	t.lock()
	if t.status == StatusDown && t.container == nil {
		t.unlock()
		return
	}
	t.setStatusL(StatusStopping, "")
	s, c := t.session, t.container
	t.unlock()
	if s != nil {
		s.Kill()
	}
	if c != nil {
		c.Rm(nestDir(t.dir())) // what its services and its sub-containers were running goes with it
	}
	t.lock()
	t.container = nil
	t.codeUp = false
	t.clearServicesL()
	t.setStatusL(StatusDown, "")
	t.unlock()
}

// Reload recreates the workspace, rebuilding the image when Containerfile.dev
// changed: what the agent gets by ending its turn with 'reload', for a human
// who edited the file themselves. A live agent turn is left alone.
func (t *Task) Reload() error {
	t.lock()
	noWorkspace := t.info.Phase == PhasePlan || !t.hasWorkspace()
	working := t.workingL()
	t.unlock()
	if noWorkspace {
		return errors.New("The task has no workspace: it is still in Plan, or finished already")
	}
	if working {
		return errors.New("The agent is working; stop it before rebuilding the container")
	}
	go func() {
		t.note("recreating the workspace container")
		t.down()
		if _, err := t.up(); err != nil {
			t.noteErr("workspace failed", err)
			return
		}
		t.queue("container", rebuiltPrompt)
	}()
	return nil
}

// --- the claude session ---

func (t *Task) ensureSession() (*ChatSession, error) {
	t.lock()
	if s := t.session; s != nil {
		t.unlock()
		return s, nil
	}
	f := t.sessionFlight
	if f == nil {
		f = &flight[*ChatSession]{done: make(chan struct{})}
		t.sessionFlight = f
		go func() {
			f.val, f.err = t.startSession()
			t.lock()
			t.sessionFlight = nil
			t.unlock()
			close(f.done)
		}()
	}
	t.unlock()
	<-f.done
	return f.val, f.err
}

func (t *Task) startSession() (*ChatSession, error) {
	c, err := t.up()
	if err != nil {
		return nil, err
	}
	t.lock()
	t.sessionBudget = t.info.Budget
	t.sessionReview = t.info.Phase == PhaseReview
	opts := SessionOpts{
		Container: c, Model: t.info.Model, System: systemPrompt, Resume: t.info.Started,
		OnEntry:  t.addEntry,
		OnUpdate: t.updateEntry,
		OnTurnStart: func() {
			t.lock()
			t.pubL("working", true)
			t.unlock()
		},
		OnTurnEnd: func(end TurnEnd) { go t.onTurnEnd(end) },
	}
	// The reviewer is the same machinery with another mind in it: its own
	// system prompt and model, and a session that is nobody's (see review.go).
	if t.sessionReview {
		opts.Model, opts.System, opts.Review, opts.Resume = cmp.Or(t.info.ReviewModel, DefaultModel), reviewSystem, true, false
	}
	if t.info.Budget != nil {
		left := max(0.01, *t.info.Budget-t.info.Spent)
		opts.Budget = &left
	}
	if !t.info.Started && !t.sessionReview { // a session that is never written down is none to resume
		t.info.Started = true
		t.p.m.saveL()
	}
	t.unlock()
	var s *ChatSession
	ready := make(chan struct{})
	opts.OnExit = func(code int, errTail string) {
		<-ready
		t.onSessionExit(s, code, errTail)
	}
	s, err = newChatSession(opts)
	close(ready)
	if err != nil {
		return nil, err
	}
	t.lock()
	t.session = s
	t.unlock()
	go t.probeContextParts(c)
	return s, nil
}

// --- waiting out claude's usage limits ---

// limitWaitL says when a task whose turn ran into a usage limit goes back in,
// and whether waiting is worth it at all: a limit that names no reset we can
// read, one more than a day out, or one that keeps coming back however long we
// wait, is the user's to sort out.
func (t *Task) limitWaitL(resetAt time.Time) (time.Time, bool) {
	t.limitWaits++
	if t.limitWaits > maxLimitWaits || resetAt.IsZero() {
		return time.Time{}, false
	}
	now := time.Now()
	until := resetAt.Add(limitSlack)
	if until.After(now.Add(limitMaxWait)) {
		return time.Time{}, false
	}
	if soonest := now.Add(limitSlack); until.Before(soonest) { // a reset already behind us
		until = soonest
	}
	return until, true
}

// armLimitL parks the task on a usage limit: it stays with the agent, and a
// timer sends the agent back in once the limit should have reset. The
// container may be recycled for idling meanwhile — the wait can be hours —
// and the moment is persisted, so a daemon that restarts picks the wait back
// up rather than dropping the task on the user. Anything else happening to the
// task (a message, a stop, a drag) clears it; see clearLimitL.
func (t *Task) armLimitL(until time.Time) {
	if t.limitTimer != nil {
		t.limitTimer.Stop()
	}
	t.info.LimitUntil = until.UnixMilli()
	t.p.m.saveL()
	t.pubL("limitUntil", t.info.LimitUntil)
	t.limitTimer = time.AfterFunc(time.Until(until), t.resumeAfterLimit)
}

// clearLimitL drops a wait that is no longer to be waited out.
func (t *Task) clearLimitL() {
	if t.limitTimer != nil {
		t.limitTimer.Stop()
		t.limitTimer = nil
	}
	if t.info.LimitUntil != 0 {
		t.info.LimitUntil = 0
		t.p.m.saveL()
		t.pubL("limitUntil", nil)
	}
}

// limitNote tells the user, in the chat the wait is silent in otherwise, that
// nothing is expected of them: the task goes on by itself.
func limitNote(until time.Time) string {
	when := until.Local().Format("15:04 MST")
	if time.Until(until) > 12*time.Hour {
		when = until.Local().Format("Jan 2, 15:04 MST")
	}
	return "claude's usage limit is reached, so this turn did not run. The task stays with the " +
		"agent and goes back in by itself at " + when + "; until the limit resets, a message of " +
		"your own would only run into it too."
}

// resumeAfterLimit sends the agent back in once the limit should have reset.
func (t *Task) resumeAfterLimit() {
	t.lock()
	t.limitTimer = nil
	if t.info.LimitUntil == 0 || !t.agentPhaseL() || t.p.tasks[t.tid] != t {
		t.unlock()
		return
	}
	t.clearLimitL()
	t.unlock()
	t.note("the usage limit should have reset; sending the agent back in")
	t.kick(limitPrompt)
}

// loginNote tells the user, in the task's own chat, what the failed turn was
// about and what to do: the sign-in button is in the sidebar, on the host,
// and signing in is all there is to it before sending the task back in.
const loginNote = "claude could not sign in on this host, so this turn did not run and nothing about it is yours " +
	"to fix: sign in to claude again — the host says so in the sidebar, and the button there does it — and send the " +
	"task back in."

// maxDoneNudges: how often in a row an agent is sent back in for the TPS-DONE
// line it forgot before the task is handed to the human anyway.
const maxDoneNudges = 2

// onTurnEnd: a claude turn finished. Account the cost, read the verdict the
// agent ended on, move the task along.
func (t *Task) onTurnEnd(end TurnEnd) {
	t.lock()
	t.touchL()
	t.p.touchL()
	save := false
	if end.Cost > 0 {
		t.info.Spent = math.Round((t.info.Spent+end.Cost)*10000) / 10000
		save = true
	}
	// How full the agent's head is, as of the request it just made. A turn
	// that compacted and made no request after says only what the summary
	// came to: that, on top of what is in the window whatever is said, stands
	// in for a measure until the next turn makes one. The reviewer's context
	// is nobody's but its own: it starts blank every time and is never
	// written down (see review.go).
	if !t.sessionReview {
		switch {
		case end.Context > 0:
			t.info.Context = end.Context
			save = true
		case end.Summary > 0:
			t.info.Context = end.Summary
			if t.info.Window != nil {
				for _, p := range t.info.Window.Parts {
					t.info.Context += p.Tokens
				}
			}
			save = true
		}
	}
	if save {
		t.p.m.saveL()
	}
	t.publishL()
	// A turn the model was never called in — a /compact's — is a turn of
	// claude's session but not one of the task's: the agent was asked for
	// nothing, so there is no verdict to read out of how it ended and no run
	// to make a save point of (see isCompact). The log heard about the
	// compaction from claude itself (see onEvent).
	if end.Idle && !end.Failed {
		t.unlock()
		return
	}
	if t.workingL() { // the user already sent a follow-up
		t.unlock()
		return
	}
	// A turn that ended because we cut it off is not a turn to read anything
	// into: whoever stopped it is seeing to what comes next.
	if t.stopping {
		t.unlock()
		return
	}
	// A turn that could not sign in says nothing about the task, whoever's turn
	// it was: the host's login is what needs fixing, so the dashboards are told
	// to ask for one and the chat says as much. Where the task goes is left to
	// the rest of this, as with any other failed turn. A verdict means the
	// agent was talking about logins rather than failing on one.
	if end.Failed && end.NoLogin && end.Done == nil {
		t.p.m.loginExpired()
		t.note(loginNote)
	}
	// The reviewer ends its turns with a review rather than a verdict, and
	// everything below is about verdicts (see review.go). A review the task has
	// moved on from — the user's message took it back — is nothing to act on.
	if t.sessionReview {
		reviewing := t.info.Phase == PhaseReview
		t.unlock()
		if reviewing {
			t.finishReview(end)
		}
		return
	}
	t.unlock()
	go t.refreshChanges()
	// Whether the turn left anything behind, for the save point it is about
	// to become and for the summary that point is labelled with.
	dirty := false
	if t.hasWorkspace() {
		dirty, _ = dirtyTree(t.repoDir())
	}
	t.lock()
	if !t.agentPhaseL() { // stopped or dragged elsewhere meanwhile
		t.unlock()
		return
	}
	next, changes := "", ""
	if end.Done != nil {
		next, changes = end.Done.Next, end.Done.Changes
		t.doneNudges = 0
	}
	if next == "reload" && !t.overBudgetL() {
		t.note("the agent asked for a container rebuild; recreating the workspace")
		s := t.session
		t.unlock()
		if s != nil {
			t.stopSession(s)
		}
		t.down()
		t.kick(reloadedPrompt)
		return
	}
	// A turn that ran into a usage limit never got to do anything: wait the
	// limit out and send the agent back in, rather than handing over a task
	// nobody has to do anything about. A verdict means the message was the
	// agent talking about limits, not claude reporting one.
	if end.Failed && end.Limited && next == "" && !t.overBudgetL() {
		if until, ok := t.limitWaitL(end.LimitAt); ok {
			t.note(limitNote(until))
			t.armLimitL(until)
			t.unlock()
			return
		}
		t.note("claude reports a usage limit that is not one to wait out here; the task is yours")
	}
	t.limitWaits = 0
	// No verdict: send the agent back in for one, unless the turn failed on its
	// own (asking again would only fail again) or it keeps forgetting.
	if next == "" && !end.Failed && !t.overBudgetL() && t.doneNudges < maxDoneNudges {
		t.doneNudges++
		t.unlock()
		t.note("the agent's turn ended without a TPS-DONE line; asking it where the task goes next")
		t.kick(donePrompt(end.Bad))
		return
	}
	// A verdict that says nothing of what the turn changed leaves the revert
	// point about to be made with an empty commit message; ask for one, as
	// long as there is something to describe and a point to be made at all.
	// A task that merges by itself makes no save point worth naming.
	autoMerging := next == "merge" && t.onReadyL() == AnswerMerge
	if next != "" && changes == "" && dirty && !autoMerging && !end.Failed && !t.overBudgetL() && t.doneNudges < maxDoneNudges {
		t.doneNudges++
		t.unlock()
		t.note("the agent's TPS-DONE line said nothing about what this turn changed; asking for a line")
		t.kick(changesPrompt)
		return
	}
	t.doneNudges = 0
	if t.info.Phase == PhaseMerge { // the agent was resolving conflicts
		msg := t.info.CommitMessage
		if next != "merge" {
			if t.overBudgetL() {
				t.noteBudgetL()
			}
			t.note("merge paused: the agent did not report the conflicts resolved")
			if len(t.info.Conflicts) > 0 { // the prompt said it; the next turn is told again
				t.queueL("conflicts", conflictsPrompt(t.info.Conflicts))
			}
			t.endRunL(changes)
			return
		}
		if m := strings.TrimSpace(end.Done.Message); m != "" {
			msg = m
		}
		t.unlock()
		t.note("conflicts resolved; merging")
		_ = t.Merge(msg)
		return
	}
	if next == "merge" {
		t.info.CommitMessage = strings.TrimSpace(end.Done.Message)
		if t.info.CommitMessage == "" {
			t.info.CommitMessage = t.info.Title
		}
		t.p.m.saveL()
		// What becomes of work the agent calls finished is the task's own answer
		// (see review.go). A review costs a turn of its own, so a task with no
		// room left for one skips to the user; a merge costs nothing.
		switch {
		case t.onReadyL() == AnswerReview && !t.overBudgetL():
			// The run's save point is made here as for any other run; the
			// review makes one of its own when it is done.
			t.unlock()
			t.mark("Agent", changes)
			t.beginReview()
			return
		case t.onReadyL() == AnswerMerge:
			msg := t.info.CommitMessage
			t.unlock()
			t.note("the agent reports the task is ready; merging")
			_ = t.Merge(msg)
			return
		}
		t.noteReadyL("the agent reports the task is ready to merge")
	}
	if t.overBudgetL() {
		t.noteBudgetL()
	}
	t.endRunL(changes)
}

// endRunL closes an agent's run: a save point on what it did (which needs the
// lock free, being all git) and the task back with the human. Expects the
// lock, and returns without it.
func (t *Task) endRunL(changes string) {
	t.unlock()
	t.mark("Agent", changes)
	t.lock()
	t.setPhaseL(PhaseHuman)
	t.unlock()
}

func (t *Task) onSessionExit(s *ChatSession, code int, errTail string) {
	t.lock()
	defer t.unlock()
	if t.session == s {
		t.session = nil
	}
	t.publishL()
	if t.info.LimitUntil > 0 {
		// Waiting out a usage limit: claude's process is not what the task is
		// waiting for (the container may well be recycled for idling before
		// the reset), and the resume starts one of its own.
		return
	}
	if t.agentPhaseL() && !t.stopping {
		// A claude that could not sign in dies before a turn of it ever runs,
		// and says so on its way out: that is the host's login rather than
		// this task, and there is nothing half-done to keep.
		if code != 0 && authGone(errTail) {
			t.p.m.loginExpired()
			t.note(loginNote)
		} else if code != 0 {
			t.note(fmt.Sprintf("claude exited unexpectedly (%d)", code), errTail)
			// Whatever it had got to is in the tree and nowhere else; a point
			// of its own is what makes it something to come back to.
			go t.mark("Agent", "claude exited part-way")
		}
		t.setPhaseL(PhaseHuman)
	}
}

// --- the forwarded ports, and whether something answers on them ---

// portsL is what the dashboard shows of the container's published ports:
// each with the loopback port it is on here (a remote dashboard swaps in a
// tunnel of its own), whether something listens there (open) and whether it
// answers HTTP (live). nil while there is no container, or it exposes nothing.
func (t *Task) portsL() any {
	if t.status != StatusUp || t.container == nil || len(t.container.Ports) == 0 {
		return nil
	}
	ports := make([]map[string]any, len(t.container.Ports))
	for i, m := range t.container.Ports {
		ports[i] = map[string]any{"port": m.Port, "host": m.Host, "live": t.live[m.Port].HTTP, "open": t.live[m.Port].Open}
	}
	return ports
}

// setLiveL records a probe round's findings (the hub drops a publish that
// changes nothing).
func (t *Task) setLiveL(live map[int]portProbe) {
	t.live = live
	t.pubL("ports", t.portsL())
}
