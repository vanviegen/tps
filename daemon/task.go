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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vanviegen/tps/hub"
)

type Phase string

const (
	PhasePlan    Phase = "plan"
	PhaseAgent   Phase = "agent"
	PhaseHuman   Phase = "human"
	PhaseMuted   Phase = "muted"   // waiting for a human too, but put away: out of the sidebar, at the top of the Plan column, and parked like a closed task
	PhaseReview  Phase = "review"  // a second agent is reading the work over before it is handed on (see review.go)
	PhaseMerging Phase = "merging" // TPS is merging: replaying the work onto the branch, or an agent resolving conflicts in it
	PhaseMerged  Phase = "merged"  // its work is on the branch and its workspace is gone
	PhaseClosed  Phase = "closed"  // closed without merging: its work is kept as a bundle, off the branch, and its workspace is gone
)

var phases = []Phase{PhasePlan, PhaseAgent, PhaseReview, PhaseHuman, PhaseMuted, PhaseMerging, PhaseMerged, PhaseClosed}

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
	idleNote      = "Workspace idle, shutting down (your work is untouched)"
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

// What a task that says nothing does: work an agent calls finished is read
// over before anyone is asked to look at it, a review asking for changes sends
// the work back to the agent once, and what a review makes of it after that is
// the user's to act on.
const (
	defaultOnReady     = AnswerReview
	defaultOnAccept    = AnswerHuman
	defaultReviewLoops = 1
)

// parseAnswer reads an answer as it arrives from a dashboard, and reports
// whether it is one of those the question offers.
func parseAnswer(raw any, offered ...Answer) (Answer, bool) {
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	if !slices.Contains(offered, Answer(s)) {
		return "", false
	}
	return Answer(s), true
}

// TaskInfo is the persisted part of a task (in projects.json).
type TaskInfo struct {
	Title         string         `json:"title"`
	Description   string         `json:"description"`     // what a plan says: the message the agent is sent in with (see Assign)
	Draft         string         `json:"draft,omitempty"` // the message being written once the task has left Plan (see setDraftL)
	Model         string         `json:"model"`
	Effort        string         `json:"effort,omitempty"`       // claude's --effort; empty or "default" is claude's own
	OnReady       Answer         `json:"onReady,omitempty"`      // what becomes of work the agent reports ready (see review.go)
	OnAccept      Answer         `json:"onAccept,omitempty"`     // and of work a review accepts
	ReviewLoops   *int           `json:"reviewLoops,omitempty"`  // how often a review asking for changes may send the work back to the agent; nil is defaultReviewLoops
	ReviewLoop    int            `json:"reviewLoop,omitempty"`   // how often it has, since the user last said something
	ReviewModel   string         `json:"reviewModel,omitempty"`  // the model the reviewer runs on; empty is the task's own model
	ReviewEffort  string         `json:"reviewEffort,omitempty"` // and its effort; empty is the task's own
	Review        string         `json:"review,omitempty"`       // the last review's feedback, waiting for the user (see review.go)
	Phase         Phase          `json:"phase"`
	Started       bool           `json:"started,omitempty"`           // the agent has a session in the task's state dir to pick back up
	CommitMessage string         `json:"commitMessage,omitempty"`     // what .tps-commit-message says, for the dashboard to show (see commitMessageFile)
	Spent         float64        `json:"spent,omitempty"`             // USD spent on agent runs so far
	Budget        *float64       `json:"budget,omitempty"`            // USD limit; the task is parked when spending reaches it
	ORBudget      *float64       `json:"openrouterBudget,omitempty"`  // USD of OpenRouter keys the agent may be handed without asking the user (see openrouter.go)
	ORGranted     float64        `json:"openrouterGranted,omitempty"` // the USD of OpenRouter spending it was granted so far
	ORKey         *orKey         `json:"openrouterKey,omitempty"`     // the key it is handed, its limit raised with every grant
	ORAsk         float64        `json:"openrouterAsk,omitempty"`     // a request of the agent's beyond that budget, waiting for the user
	Suggestion    map[string]any `json:"suggestion,omitempty"`        // project settings the agent suggests, waiting for the user (see settings.go)
	Context       int64          `json:"context,omitempty"`           // tokens the conversation came to at the agent's last turn
	Window        *ContextWindow `json:"window,omitempty"`            // and what claude's window holds besides it (see context.go)
	TitleAsked    bool           `json:"titleAsked,omitempty"`        // claude has been asked to name this task (once is enough)
	PhaseAt       int64          `json:"phaseAt,omitempty"`           // ms epoch of the last phase change; boards show the freshest first
	WaitFor       []string       `json:"waitFor,omitempty"`           // tids this task waits for: until they are all done, no agent works in it (see waitingL)
	Deferred      string         `json:"deferred,omitempty"`          // what the user said meanwhile, which the agent is sent in with once the wait is over (see endWait)
	LimitUntil    int64          `json:"limitUntil,omitempty"`        // ms epoch the agent goes back in at, waiting out a usage limit (see armLimitL)
	SkipTests     bool           `json:"skipTests,omitempty"`         // its merge lands without running the project's test (see queue.go)
	Stamp         string         `json:"stamp,omitempty"`             // what the commits it lands carry as their TPS-Task trailer (see branch.go)
	Schedule      *Schedule      `json:"schedule,omitempty"`          // a scheduled task: it stays in Plan, and runs as tasks of its own (see schedule.go)
	Scheduled     string         `json:"scheduled,omitempty"`         // the scheduled task this one is a run of, which nobody watches
	RunAt         int64          `json:"runAt,omitempty"`             // ms epoch its schedule started it at
	Overruns      int            `json:"overruns,omitempty"`          // how often such a run had its budget raised to wrap up in (see overrunL)
	Continue      bool           `json:"continue,omitempty"`          // its agent's last turn ended on merge-and-continue (see landed)
	AfterMerge    bool           `json:"afterMerge,omitempty"`        // it is merged, and its agent was sent back in as it asked (see carryOn)
	AwaitDeploy   string         `json:"awaitDeploy,omitempty"`       // the commit it merged as, whose deploy sends the agent back in (see landed)
	Requested     string         `json:"requested,omitempty"`         // the task whose agent asked for this one, until the user has opened it (see board.go)

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

// shareL runs do as the flight in *slot, or joins the one there already, and
// waits for its outcome. Expects the lock, and returns without it.
func shareL[T any](t *Task, slot **flight[T], do func() (T, error)) (T, error) {
	f := *slot
	if f == nil {
		f = &flight[T]{done: make(chan struct{})}
		*slot = f
		go func() {
			f.val, f.err = do()
			t.lock()
			*slot = nil
			t.unlock()
			close(f.done)
		}()
	}
	t.unlock()
	<-f.done
	return f.val, f.err
}

// Task: its workspace is a plain clone of the project repo (git hardlinks the
// object store, so cloning is nearly free and safe: object files are never
// modified in place), created when the task leaves the plan phase and kept
// until the task finishes or is discarded back to plan. A sibling dir holds
// the claude conversation state, another file the condensed chat log; a
// parked task has those compressed into one archive, and one closed or muted
// its work as a bundle beside it (see park).
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
	codeUp       bool  // code-server runs in the container
	codeSyncing  bool  // a syncCode is under way
	codeStart    int64 // ms epoch of the code-server start: a new one means VS Code must reload
	codeError    string
	live         map[int]portProbe // by container port: what answers there (see checkLive)
	checkingLive bool
	refreshing   bool     // a changes overview is being computed
	bundled      []change // what the bundle beside a parked task changes, once read (see bundleChanges); cloneMu guards it
	ending       bool     // its wait is being ended (see endWait), so it isn't ended twice
	behind       int      // commits on the default branch the workspace doesn't have yet (see refreshBehind)
	merging      bool     // its merge is under way: TPS at it, or the agent it sent in (see queue.go)
	testing      bool     // and runs the project's test
	testRuns     int      // the times that test failed in this merge
	held         string   // the commit it is to become, held back by the project checkout's changes

	session       *Session
	sessionFlight *flight[*Session]
	sessionBudget *float64 // the budget setting the running agent was started under
	sessionSystem string   // and the system prompt
	sessionMind   mind     // and the model and effort
	sessionReview bool     // and whether it is the reviewer rather than the task's own agent (see review.go)
	upFlight      *flight[*Container]
	lastTag       string // image tag of the Containerfile the container was brought up for
	stopping      bool
	statusNudges  int             // turns in a row the agent was sent back in for a status line its turn left out or got wrong
	limitTimer    *time.Timer     // running while the task waits out a usage limit (see armLimitL)
	limitWaits    int             // waits in a row that ran straight into the limit again
	boardBusy     map[string]bool // the requests about other projects being answered (see board.go)
	chatMu        sync.Mutex
	cloneMu       sync.Mutex // one workspace at a time — two messages can want one at once — and none dropped while it is read (see refreshChanges)
	starting      sync.Mutex // held while a task sent in from Plan has its workspace made, until the plan is in the log (see Assign)

	declared      []declaredService // the services Containerfile.dev names, known along with the container
	services      []serviceState    // their state and that of the ad hoc ones, as last read (see services.go)
	logsPublished map[string]bool   // the services whose output tail the dashboard has
	orBusy        bool              // an OpenRouter key is being made for the agent (see openrouter.go)
	settingsShown string            // the project's settings as last written for the tool to read (see syncSettingsL)
}

func newTask(p *Project, tid string, info *TaskInfo) *Task {
	return &Task{p: p, tid: tid, info: info, status: StatusDown, lastActivity: time.Now()}
}

func (t *Task) key() string           { return t.p.pid + "/" + t.tid }
func (t *Task) dir() string           { return filepath.Join(t.p.tasksDir(), t.tid) }
func (t *Task) repoDir() string       { return filepath.Join(t.dir(), "repo") }
func (t *Task) chatFile() string      { return filepath.Join(t.dir(), "chat.jsonl") }
func (t *Task) bundleFile() string    { return filepath.Join(t.dir(), "work.bundle") }
func (t *Task) archiveFile() string   { return filepath.Join(t.dir(), "archive.tar.gz") }
func (t *Task) containerName() string { return "tps-" + t.p.pid + "-" + t.tid }
func (t *Task) workingL() bool        { return t.session != nil && t.session.TurnActive() }
func (t *Task) touchL()               { t.lastActivity = time.Now() }

// claudeDir is where the task keeps what claude remembers: its session
// transcripts, and whatever else it writes beside them (see claudeMounts).
func (t *Task) claudeDir() string { return filepath.Join(t.dir(), "claude") }

// errNoWorkspace turns down what needs a workspace in a task that has none.
var errNoWorkspace = errors.New("The task has no workspace: it is still in Plan, or finished already")

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

// reviewLoops: how often a loop-back limit lets a review send the work back.
func reviewLoops(loops *int) int {
	if loops == nil {
		return defaultReviewLoops
	}
	return *loops
}

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
	return t.info.Phase == PhaseAgent || t.info.Phase == PhaseReview || t.info.Phase == PhaseMerging
}

// toHumanL hands a task an agent may be at work in to the user.
func (t *Task) toHumanL() {
	if t.agentPhaseL() {
		t.setPhaseL(PhaseHuman)
	}
}

// phase is the task's phase, for a caller that holds no lock.
func (t *Task) phase() Phase {
	t.lock()
	defer t.unlock()
	return t.info.Phase
}

// systemPromptL is what the task's own agent is told above its conversation.
func (t *Task) systemPromptL() string {
	if t.p.info.Scratch {
		return scratchPrompt
	}
	if t.info.Scheduled != "" {
		return scheduledIntro + agentRules + turnRule
	}
	return systemPrompt
}

// mergesByItselfL: work the agent reports ready is merged without the user,
// after a review where the task is set to have one.
func (t *Task) mergesByItselfL() bool {
	return t.onReadyL() == AnswerMerge || (t.onReadyL() == AnswerReview && t.onAcceptL() == AnswerMerge)
}

func (t *Task) lock()   { t.p.m.mu.Lock() }
func (t *Task) unlock() { t.p.m.mu.Unlock() }

func (t *Task) pubL(field string, value any) {
	t.p.m.hub.Set([]string{"projects", t.p.pid, "tasks", t.tid, field}, value)
}

// orNil is a value to publish, or nil (deleting the field) for its zero.
func orNil[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

func (t *Task) publishL() {
	t.pubL("title", t.info.Title)
	if t.job() {
		t.pubL("job", t.tid)
	}
	t.pubL("description", t.info.Description)
	t.pubL("requested", orNil(t.info.Requested))
	t.pubL("draft", orNil(t.info.Draft))
	t.pubL("model", t.info.Model)
	t.pubL("effort", orNil(t.info.Effort))
	t.pubL("onReady", string(t.onReadyL()))
	t.pubL("onAccept", string(t.onAcceptL()))
	if prompt := t.systemPromptL(); prompt != systemPrompt { // the host's (see server.go)
		t.pubL("agentPrompt", prompt)
	} else {
		t.pubL("agentPrompt", nil)
	}
	t.pubL("reviewLoops", reviewLoops(t.info.ReviewLoops))
	t.pubL("reviewModel", orNil(t.info.ReviewModel))
	t.pubL("reviewEffort", orNil(t.info.ReviewEffort))
	t.pubL("settingsPending", orNil(t.session != nil && t.sessionStaleL())) // see kick
	t.pubL("review", orNil(t.info.Review))
	t.pubL("phase", t.info.Phase)
	t.pubL("phaseAt", t.info.PhaseAt)
	t.pubL("afterMerge", orNil(t.info.AfterMerge))
	t.pubL("commitMessage", orNil(t.info.CommitMessage))
	t.pubL("skipTests", t.info.SkipTests)
	t.pubL("spent", orNil(t.info.Spent))
	t.pubL("budget", optional(t.info.Budget))
	t.pubL("openrouterBudget", optional(t.info.ORBudget))
	t.pubL("openrouterGranted", orNil(t.info.ORGranted))
	t.pubL("openrouterAsk", orNil(t.info.ORAsk))
	if t.info.Suggestion != nil {
		t.pubL("suggestion", t.info.Suggestion)
	} else {
		t.pubL("suggestion", nil)
	}
	t.pubL("context", t.contextL())
	t.pubL("schedule", optional(t.info.Schedule))
	t.pubL("scheduled", orNil(t.info.Scheduled))
	t.pubL("runAt", orNil(t.info.RunAt))
	if len(t.info.WaitFor) > 0 {
		t.pubL("waitFor", t.info.WaitFor)
	} else {
		t.pubL("waitFor", nil)
	}
	t.pubL("deferred", orNil(t.info.Deferred))
	// Whether there is a workspace to open at all: a task in Plan has none yet,
	// a finished one no longer.
	t.pubL("worktree", orNil(t.hasWorkspace()))
	t.pubL("behind", orNil(t.behind))
	t.pubL("limitUntil", orNil(t.info.LimitUntil))
	t.pubL("status", t.status)
	t.pubL("statusDetail", t.statusDetail)
	t.pubL("working", t.workingL())
	t.pubL("testing", orNil(t.testing))
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
	t.pubL("codeError", orNil(t.codeError))
}

// parseBudget reads a spending limit as it arrives from a dashboard: a number
// or the text of a number field. Anything that is not a limit (empty, zero,
// nonsense) means no limit at all.
func parseBudget(raw any) *float64 {
	budget, _ := parseNumber(raw)
	if budget <= 0 || math.IsInf(budget, 0) {
		return nil
	}
	return &budget
}

// maxReviewLoops caps how often a review may send the work back: a limit is
// there to end the back-and-forth, and one this long has stopped being
// something to leave running.
const maxReviewLoops = 10

// parseLoops reads that limit as it arrives from a dashboard: a number or the
// text of one, and reports whether it was a count at all. Anything that is not
// leaves the limit as it stands.
func parseLoops(raw any) (int, bool) {
	loops, ok := parseNumber(raw)
	if !ok {
		return 0, false
	}
	return min(max(int(loops), 0), maxReviewLoops), true
}

// parseNumber reads a number as a dashboard sends one: as it is, or as the
// text of a number field.
func parseNumber(raw any) (float64, bool) {
	var n float64
	switch v := raw.(type) {
	case float64:
		return v, true
	case string:
		if c, _ := fmt.Sscanf(strings.TrimSpace(v), "%g", &n); c == 1 {
			return n, true
		}
	}
	return 0, false
}

// optional turns a nil pointer into nil (deleting the field) and otherwise the value.
func optional[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

// copyPtr: a pointer setting of one's own, so changing it leaves the setting
// it was copied from alone.
func copyPtr[T any](v *T) *T {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func (t *Task) setStatusL(status WorkStatus, detail string) {
	t.status, t.statusDetail = status, detail
	if status != StatusUp {
		t.live = nil
	}
	t.publishL()
}

func (t *Task) setPhaseL(phase Phase) {
	if phase != PhaseAgent && phase != PhaseMerging { // a task leaving the agent (the review included) waits for nothing
		t.clearLimitL()
	}
	switch {
	case t.job(): // a job's Merge is its running (see jobs.go)
	case phase == PhaseMerging:
		t.p.enqueueL(t)
	default:
		t.p.dequeueL(t)
	}
	// A task being merged, finished or put away waits for nothing any longer:
	// what was held back for its agent goes back to the composer.
	if phase == PhaseMerging || phase == PhaseMerged || phase == PhaseClosed || phase == PhaseMuted {
		t.info.WaitFor = nil
		if t.info.Deferred != "" {
			t.info.Draft = joinMessages(t.info.Deferred, t.info.Draft)
			t.info.Deferred = ""
		}
	}
	t.info.Phase = phase
	t.info.PhaseAt = time.Now().UnixMilli()
	t.p.touchL()
	t.p.m.saveL()
	t.publishL()
	t.p.endWaitsL() // a task reaching Done may be the last one another was waiting for
	go t.syncCode() // a workspace it just got (or lost) changes what VS Code can show
}

// --- chat log (no manager lock needed) ---

func (t *Task) addEntry(e *ChatEntry) {
	t.p.m.hub.Chat(t.key(), hub.Entry{ID: e.ID, K: e.K, V: e})
	t.appendChat(e)
}

// updateEntry re-sends a changed entry (a tool call that got its result); it replaces by id.
func (t *Task) updateEntry(e *ChatEntry) {
	t.p.m.hub.ChatUpdate(t.key(), hub.Entry{ID: e.ID, K: e.K, V: e})
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

// serviceNote is a note about a run of a service, which opens its console
// for as long as that run is the service's latest.
func (t *Task) serviceNote(s serviceState, text string) {
	e := newEntry("note")
	e.Text = text
	e.Service, e.Run = s.Name, s.Started
	t.addEntry(e)
}

// lastNote is the text of the chat log's newest entry, if that is a note: one
// made since the log was read in, or one read back from the file.
func (t *Task) lastNote() string {
	e, ok := t.p.m.hub.LastChat(t.key())
	if !ok {
		return ""
	}
	var c ChatEntry
	switch v := e.V.(type) {
	case *ChatEntry:
		c = *v
	case json.RawMessage:
		_ = json.Unmarshal(v, &c)
	}
	if c.K != "note" {
		return ""
	}
	return c.Text
}

func (t *Task) noteErr(prefix string, err error) { t.note(prefix + ": " + err.Error()) }

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
		if !json.Valid(line) {
			continue
		}
		e := hub.RawEntry(json.RawMessage(strings.Clone(string(line))))
		if i, ok := byID[e.ID]; ok && e.ID != "" {
			entries[i] = e
		} else {
			if e.ID != "" {
				byID[e.ID] = len(entries)
			}
			entries = append(entries, e)
		}
	}
	return entries
}

// ChatTurn is the n-th turn of the chat log in full, where what the hub holds
// of it may have gaps (see hub.fold): a message of the user's and all that
// followed it, turn 0 being what came before the first.
func (t *Task) ChatTurn(n int) []any {
	turn := 0
	out := []any{}
	for _, e := range t.readChat() {
		if e.K == "user" {
			turn++
		}
		if turn == n {
			out = append(out, e.V)
		}
	}
	return out
}

// --- settings ---

// Update applies a dashboard's settings. A task set to wait while an agent
// works in it has the agent stopped: nothing is to happen in it until the
// wait is over.
func (t *Task) Update(partial map[string]any) error {
	t.lock()
	waiting := t.waitingL()
	t.applyL(partial)
	t.p.touchL()
	t.p.m.saveL()
	t.publishL()
	t.p.endWaitsL()
	hold := !waiting && t.blockedL() && t.info.Phase != PhasePlan
	stop := hold && t.agentPhaseL()
	t.unlock()
	if hold {
		t.note("Waiting for other tasks to be done; what is said meanwhile is held back for the agent until then")
	}
	if stop {
		_ = t.StopAgent()
	}
	return nil
}

// applyL sets whatever the partial names, leaving the rest alone.
func (t *Task) applyL(partial map[string]any) {
	if title, ok := partial["title"].(string); ok && strings.TrimSpace(title) != "" {
		t.info.Title = strings.TrimSpace(title)
	}
	if draft, ok := partial["draft"].(string); ok {
		t.setDraftL(draft)
	}
	if model, ok := partial["model"].(string); ok {
		t.info.Model = model
	}
	if model, ok := partial["reviewModel"].(string); ok {
		t.info.ReviewModel = model
	}
	if effort, ok := partial["effort"].(string); ok {
		t.info.Effort = effort
	}
	if effort, ok := partial["reviewEffort"].(string); ok {
		t.info.ReviewEffort = effort
	}
	if answer, ok := parseAnswer(partial["onReady"], AnswerHuman, AnswerReview, AnswerMerge); ok {
		t.info.OnReady = answer
	}
	if answer, ok := parseAnswer(partial["onAccept"], AnswerHuman, AnswerMerge); ok {
		t.info.OnAccept = answer
	}
	if loops, ok := parseLoops(partial["reviewLoops"]); ok {
		t.info.ReviewLoops = &loops
	}
	if raw, ok := partial["budget"]; ok {
		t.info.Budget = parseBudget(raw)
	}
	if raw, ok := partial["openrouterBudget"]; ok {
		t.info.ORBudget = parseBudget(raw)
	}
	if skip, ok := partial["skipTests"].(bool); ok {
		t.info.SkipTests = skip
		if skip && t.testing {
			go t.guestTool("stop", testService) // the merge goes on without it (see mergeHead)
		}
	}
	if raw, ok := partial["schedule"].(map[string]any); ok && t.info.Schedule != nil {
		t.info.Schedule.apply(raw)
	}
	// A task on its way out, a scheduled one and a job have nothing to wait for
	// (see setPhaseL).
	if raw, ok := partial["waitFor"].([]any); ok && t.info.Schedule == nil && !t.job() && !t.finishedL() &&
		t.info.Phase != PhaseMerging && t.info.Phase != PhaseMuted {
		var tids []string
		seen := map[string]bool{}
		for _, d := range raw {
			s, ok := d.(string)
			if !ok || seen[s] || s == t.tid || t.p.tasks[s] == nil || t.p.waitsForL(s, t.tid, nil) { // dropping cycles keeps waits endable
				continue
			}
			seen[s] = true
			tids = append(tids, s)
		}
		t.info.WaitFor = tids
	}
	// Taken back to the composer, or reworded: only while it is held back.
	if deferred, ok := partial["deferred"].(string); ok && t.waitingL() {
		t.info.Deferred = strings.TrimSpace(deferred)
	}
}

// finishedL: the task is over, merged or closed without merging. Nothing
// waits for it any longer, and no agent is sent into it.
func (t *Task) finishedL() bool {
	return t.info.Phase == PhaseMerged || t.info.Phase == PhaseClosed
}

// blockedL: some task this one waits for is not done yet. A deleted one is
// nothing to wait for.
func (t *Task) blockedL() bool {
	for _, tid := range t.info.WaitFor {
		if dep := t.p.tasks[tid]; dep != nil && !dep.finishedL() {
			return true
		}
	}
	return false
}

// waitingL: the task waits for others, or the wait is being ended. Meanwhile no
// agent is sent into it, and what the user says is held back as Deferred, for
// the agent to be sent in with once the wait is over (see endWait). A plan
// stays in Plan until then, so there is no half-made workspace anywhere.
func (t *Task) waitingL() bool { return len(t.info.WaitFor) > 0 || t.ending }

// waitOverL: the tasks it waits for are all done, or a message is held back
// with nothing left to wait for (the wait was taken off by hand), and nobody
// is ending the wait already.
func (t *Task) waitOverL() bool {
	if t.ending {
		return false
	}
	if len(t.info.WaitFor) == 0 {
		return t.info.Deferred != ""
	}
	return !t.blockedL()
}

// endWait sends the agent in once the wait is over. A plan is sent in with the
// message held back as its first — one nobody sent stays in Plan, with nothing
// to wait for. A task that left Plan is rebased onto the latest default branch
// first, which brings in the work it waited for, and the agent carries on with
// the message held back, or without one: the agent stopped for the wait (see
// Update) is resumed. Anything said meanwhile is held back too, and goes with
// it; a wait taken up again meanwhile goes on.
func (t *Task) endWait() {
	defer t.p.m.work()()
	if t.phase() != PhasePlan {
		_, _ = t.rebase()
	}
	t.lock()
	t.ending = false
	waited, text := len(t.info.WaitFor) > 0, t.info.Deferred
	if waited && t.blockedL() { // waiting again: the conflicts of the rebase wait with the rest
		t.toHumanL()
		t.unlock()
		return
	}
	if !waited && text == "" { // sent in by hand meanwhile (see Assign)
		t.unlock()
		return
	}
	t.info.WaitFor, t.info.Deferred = nil, ""
	t.p.m.saveL()
	t.publishL()
	switch {
	case t.info.Phase == PhasePlan:
		if text != "" {
			unsent := t.info.Description
			t.info.Description = text
			t.sendInL(text)
			t.setDraftL(unsent) // out of Plan, the composer's own again
		}
		t.unlock()
	default:
		t.unlock()
		if text == "" {
			t.kick(waitedPrompt)
		} else {
			t.say(text)
		}
	}
}

// deferL holds a message of the user's back until the wait is over, after any
// held back already. In Plan, what was sent is the plan's no longer: the task
// is named after it, and the composer is empty again.
func (t *Task) deferL(text string) {
	if t.info.Phase == PhasePlan {
		t.ensureTitleL(text)
		t.info.Description = ""
		t.pubL("description", "")
	}
	t.info.Deferred = joinMessages(t.info.Deferred, text)
	t.p.m.saveL()
	t.pubL("deferred", t.info.Deferred)
}

// joinMessages puts messages one after the other, leaving out empty ones.
func joinMessages(texts ...string) string {
	var kept []string
	for _, s := range texts {
		if s = strings.TrimSpace(s); s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, "\n\n")
}

// overBudgetL: the budget (if any) leaves no meaningful room for another run.
func (t *Task) overBudgetL() bool {
	return t.info.Budget != nil && *t.info.Budget-t.info.Spent < 0.01
}

func (t *Task) noteBudgetL() {
	t.note(fmt.Sprintf("The task budget ($%g) is used up (spent $%.2f); raise it in the task settings to continue", *t.info.Budget, t.info.Spent))
	t.queueL("budget", budgetPrompt(*t.info.Budget, t.info.Spent))
}

// --- phase transitions ---

// ensureTitleL names a task once its plan is closed, or on its way out of Plan,
// after what the plan says. It has been calling itself after that all along
// (see setDescriptionL), which will do; the agent is asked for something
// better in the background, so nothing waits on the naming. That happens once
// in a task's life, and the answer is adopted only while the stand-in is still
// there — a rename meanwhile wins.
func (t *Task) ensureTitleL(desc string) {
	if strings.TrimSpace(desc) == "" {
		return
	}
	stand := draftTitle(desc)
	if t.info.Title == "" {
		t.setTitleL(stand)
	}
	if t.info.TitleAsked {
		return
	}
	t.info.TitleAsked = true
	t.p.m.saveL()
	if t.info.Title != stand {
		return // named by hand already: leave it be
	}
	dir, branch := t.p.dir(), t.p.defaultBranch
	go func() {
		subjects, _ := git(dir, "log", "-n", "10", "--format=%s", branch)
		title := generateTitle(desc, subjects)
		t.lock()
		defer t.unlock()
		if title != "" && t.info.Title == stand && t.p.tasks[t.tid] == t { // not renamed, not deleted meanwhile
			t.setTitleL(title)
		}
	}()
}

func (t *Task) setTitleL(title string) {
	t.info.Title = title
	t.p.m.saveL()
	t.pubL("title", title)
}

// setDescriptionL records what the plan says. Until the task is named for
// real — by the agent once its plan is closed, or by hand — its title follows
// it, so the board says what it is about from the first words written.
func (t *Task) setDescriptionL(desc string) {
	if desc == t.info.Description {
		return
	}
	if t.info.Title == "" || t.info.Title == draftTitle(t.info.Description) {
		t.info.Title = draftTitle(desc)
		t.pubL("title", t.info.Title)
	}
	t.info.Description = desc
	t.p.m.saveL()
	t.pubL("description", desc)
}

// setDraftL keeps what the composer holds, unsent, with the task, so that it
// is there in any dashboard and after any time away; files attached to it are
// stored as they are added (see Attach), and it refers to them by path. In
// Plan, what is written is the plan itself, which the agent was given stays.
func (t *Task) setDraftL(draft string) {
	if t.info.Phase == PhasePlan {
		t.setDescriptionL(strings.TrimSpace(draft))
		return
	}
	if draft == t.info.Draft {
		return
	}
	t.info.Draft = draft
	t.p.m.saveL()
	t.pubL("draft", orNil(draft))
}

// offerDraftL puts a message in the composer for the user to send as it
// stands, word differently, or clear: one they are part-way through writing
// wins.
func (t *Task) offerDraftL(text string) {
	if strings.TrimSpace(t.info.Draft) == "" {
		t.setDraftL(text)
	}
}

// Assign leaves the plan phase: hand the task over, the agent with the plan as
// its first message — text, where that is what was sent (see SendChat). A task
// set to wait for others stays in Plan until they are done (see endWait), and
// starts from the branch with their work in it (see freshen); handing it to
// the agent meanwhile is the way to start it anyway, without waiting, the
// message held back going first. Handed to the human, it waits on.
func (t *Task) Assign(to, text string) error {
	t.lock()
	if t.info.Schedule != nil {
		t.unlock()
		return errScheduled
	}
	if text != "" {
		t.setDescriptionL(text) // the composer's debounce may owe the server the last keystroke
	}
	desc := t.info.Description
	if to == "agent" {
		desc = joinMessages(t.info.Deferred, desc)
	}
	if desc == "" && t.info.Deferred == "" {
		t.unlock()
		return errors.New("Write the plan first")
	}
	t.ensureTitleL(desc)
	t.touchL()
	if to == "agent" {
		t.info.WaitFor, t.info.Deferred = nil, ""
		t.info.Description = desc
		t.sendInL(desc)
		t.unlock()
		return nil
	}
	t.unlock()
	defer t.p.m.work()()
	if err := t.prepare(); err != nil {
		return err
	}
	t.lock()
	t.setPhaseL(PhaseHuman)
	t.unlock()
	t.bgUp()
	return nil
}

// sendInL hands a plan to the agent, desc its first message. The task leaves
// Plan at once, and its workspace is made in the background: a clone and a
// fetch can take a while, and the plan only shows in the log behind the save
// point made on that workspace (see prepare).
func (t *Task) sendInL(desc string) {
	t.setPhaseL(PhaseAgent)
	t.starting.Lock() // before the unlock, so that a message sent next waits for the plan (see SendChat)
	go func() {
		defer t.starting.Unlock()
		defer t.p.m.work()()
		if err := t.prepare(); err != nil {
			t.noteErr("Preparing the workspace failed", err)
			t.lock()
			t.setPhaseL(PhasePlan)
			t.unlock()
			return
		}
		t.say(desc)
	}()
}

// prepare gives a task leaving Plan its workspace, on the latest default branch.
func (t *Task) prepare() error {
	if err := t.ensureWorkspace(); err != nil {
		return err
	}
	if err := t.freshen(); err != nil {
		t.noteErr("Bringing the workspace onto the latest "+t.p.defaultBranch+" failed", err)
	}
	// A point before the first word is said, so that the first word can be
	// taken back like any other: rewinding to it empties the log and hands the
	// message back to the composer.
	t.mark("Start", "")
	return nil
}

// isCompact: the message is the agent's own /compact — the user asking it to
// summarise what it remembers down to a paragraph, with or without a word on
// what to keep — rather than a message for the agent. It goes down the same
// road as one, but nothing is asked of the agent in it: the task stays whose
// it was, and nothing is read from how the turn ends (see kick, onTurnEnd).
func isCompact(text string) bool {
	return text == "/compact" || strings.HasPrefix(text, "/compact ")
}

// SendChat: a user chat message, shown in the log, then fed to (or starting)
// the agent — or held back while the task waits for others (see waitingL).
// Files attached to it are in the task's uploads directory already, and the
// message refers to them by the path it reads them at (see Attach). A message
// to a task in Plan is the plan, and sends it to the agent.
func (t *Task) SendChat(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	compact := isCompact(text)
	t.lock()
	plan := t.info.Phase == PhasePlan
	switch {
	case compact && plan:
		t.unlock()
		return errors.New("The agent has not been sent in yet: there is nothing to compact")
	// A message sent while the agent works steers the turn under way, and one
	// sent while it compacts is queued behind that; a /compact is neither,
	// and an agent sent one mid-turn drops it without a word.
	case compact && t.workingL():
		t.unlock()
		return errors.New("The agent is busy; wait for the turn to end, or stop it first")
	// The session a review runs is the reviewer's, and that one is nobody's
	// conversation (see review.go); the agent has the task back soon enough.
	case compact && t.info.Phase == PhaseReview:
		t.unlock()
		return errors.New("A review is under way; this is the agent's own memory, and it has the task back after")
	// A message is the turn a wait for the limit was for, or what replaces it
	// (see kick); a /compact is neither, and would run into the limit itself.
	case compact && t.info.LimitUntil > 0:
		t.unlock()
		return errors.New("The task is waiting out a usage limit; the compaction would run into it too")
	}
	if t.waitingL() {
		t.deferL(text)
		t.unlock()
		return nil
	}
	t.unlock()
	// A message picks a parked task back up for the agent (see kick); a
	// /compact asks nothing of it, so the task is picked up as the human's.
	if compact && !t.hasWorkspace() {
		if err := t.reopen(); err != nil {
			return err
		}
	}
	if plan {
		return t.Assign("agent", text)
	}
	t.lock()
	t.touchL()
	t.p.touchL()
	// A word from the user is a fresh start for the automatic review: its
	// rounds are counted per thing asked for, and the feedback that was waiting
	// here has been answered, ignored, or sent back in — either way it is said.
	// A /compact says nothing to anyone, and leaves it all standing.
	if !compact {
		t.statusNudges = 0
		t.info.ReviewLoop = 0
		t.setReviewL("")
	}
	// A review is a second opinion, and the user's own word outranks it: the
	// task is the agent's again, so the kick below swaps the reviewer out for it.
	if t.info.Phase == PhaseReview {
		t.setPhaseL(PhaseAgent)
	}
	t.unlock()
	t.starting.Lock() // the plan goes first
	t.starting.Unlock()
	t.say(text)
	return nil
}

// say puts a message of the user's into the log, and gives it to the agent.
func (t *Task) say(text string) {
	e := newEntry("user")
	e.Text = text
	t.addEntry(e)
	t.kick(text)
}

// mindL is the model and effort the task's agent runs on, or its reviewer,
// whose settings left empty are the agent's.
func (t *Task) mindL(review bool) mind {
	if review {
		return mind{cmp.Or(t.info.ReviewModel, t.info.Model), cmp.Or(t.info.ReviewEffort, t.info.Effort)}
	}
	return mind{t.info.Model, t.info.Effort}
}

// sessionStaleL: the running session's spending cap, system prompt, model or
// effort, all fixed at its start, are no longer what the task says.
func (t *Task) sessionStaleL() bool {
	return !sameBudget(t.sessionBudget, t.info.Budget) || t.sessionSystem != t.systemPromptL() || t.sessionMind != t.mindL(t.sessionReview)
}

func sameBudget(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// kick makes sure an agent session is running and feeds it text, in the
// background. A task being merged stays in that phase: the agent then works
// on the merge. A finished task picks the conversation up where it ended, in a
// workspace freshly made from the branch (see ensureWorkspace); the notes
// waiting for the agent tell it what happened meanwhile.
func (t *Task) kick(text string) {
	if t.job() {
		return // no agent works in a job, the deploy's credentials least of all (see jobs.go)
	}
	t.lock()
	// No agent goes into a task that waits for others: what it was to be told
	// is in its notes, or held back for when the wait is over (see endWait).
	if t.waitingL() {
		t.unlock()
		return
	}
	t.clearLimitL()  // a turn now is what the wait was for, or what replaces it
	t.holdL("", nil) // the work is about to change: a merge held back makes its commit anew
	if t.overBudgetL() && !t.overrunL() {
		t.noteBudgetL()
		t.toHumanL()
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
	if !compact && t.info.Phase != PhaseMerging && t.info.Phase != PhaseReview {
		t.setPhaseL(PhaseAgent)
	}
	// A running session is the reviewer's or the task's own agent's: the other
	// one taking over needs a new process. So does a session gone stale, but
	// one mid-turn is steered rather than cut short, and made anew for the
	// turn after.
	review := t.info.Phase == PhaseReview
	old := t.session
	restart := old != nil && (t.sessionReview != review || (!old.TurnActive() && t.sessionStaleL()))
	t.unlock()
	go func() {
		if restart {
			t.stopSession(old)
		}
		if pickedUp {
			t.notePickedUp()
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
	t.toHumanL()
}

func (t *Task) stopSession(s *Session) {
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

// probeContextParts learns what the agent's window holds before a word is said
// in it, and where the agent will compact — the two things the dashboard's
// gauge needs and no turn reports (see context.go). Neither changes while the
// model does not, so a window already measured for it is left alone; the asking
// is done in the background, a task waiting on it waiting on nothing it needs.
func (t *Task) probeContextParts(c *Container) {
	t.lock()
	model := t.info.Model
	// The reviewer's window is its own, and starts empty every time (see
	// review.go): what it holds says nothing about the task's conversation.
	if t.sessionReview || (t.info.Window != nil && t.info.Window.Model == model) {
		t.unlock()
		return
	}
	system := t.systemPromptL()
	t.unlock()
	p, ok := probeContext(c, model, system)
	if !ok {
		return
	}
	p.Model = model
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
	t.toHumanL()
	t.unlock()
	return cutOff
}

// MoveTo maps board drags onto the real actions. Dropped on Done, a task is
// merged; Closed is where it goes when it should not be (see Close).
func (t *Task) MoveTo(phase Phase) error {
	t.lock()
	current, scheduled := t.info.Phase, t.info.Schedule != nil
	t.unlock()
	if scheduled {
		return errScheduled
	}
	if !slices.Contains(phases, phase) || phase == current {
		return nil
	}
	// A muted task is parked: going anywhere it is worked on brings its
	// workspace back first.
	if current == PhaseMuted && phase != PhasePlan && phase != PhaseClosed {
		if err := t.reopen(); err != nil {
			return err
		}
	}
	switch phase {
	case PhasePlan:
		return t.Discard()
	case PhaseAgent:
		if current == PhasePlan {
			return t.Assign("agent", "")
		}
		// Sent in by hand, without waiting: what was held back goes with it, on
		// the workspace as it stands (see Assign).
		t.lock()
		text := joinMessages(t.info.Deferred, "Please continue working on the task.")
		t.info.WaitFor, t.info.Deferred = nil, ""
		t.p.m.saveL()
		t.publishL()
		t.unlock()
		return t.SendChat(text)
	case PhaseReview:
		return t.StartReview()
	case PhaseHuman, PhaseMuted:
		// Both wait for a human; muting is that with the task put away. Getting
		// there is the same work either way — a task coming from Plan or a
		// closed one has a workspace made first — and a muted one is parked at
		// the end.
		switch current {
		case PhasePlan:
			if err := t.Assign("human", ""); err != nil {
				return err
			}
		case PhaseClosed: // muted, it stays parked as it is
			if phase == PhaseHuman {
				if err := t.reopen(); err != nil {
					return err
				}
			}
		case PhaseAgent, PhaseReview, PhaseMerging:
			if err := t.StopAgent(); err != nil {
				return err
			}
		}
		t.lock()
		if t.info.Phase != phase {
			t.setPhaseL(phase)
		}
		t.unlock()
		if phase == PhaseMuted {
			t.park()
		}
		return nil
	case PhaseClosed:
		return t.Close()
	default:
		return t.Merge("", false)
	}
}

// Close puts a task away without merging it: its work is kept as a bundle,
// off the default branch (see park), and comes back when the task is picked
// up again — or is thrown out with the task. The dashboard offers this
// beside merging, and recommends merging.
func (t *Task) Close() error {
	defer t.p.m.work()()
	t.stopAgent()
	t.lock()
	t.setCommitMessageL("")
	t.setPhaseL(PhaseClosed)
	t.unlock()
	t.park()
	return nil
}

// commitMessageFile is where the agent keeps the message for the commit its
// task will become: a file in the workspace, so that it says what the whole
// task has done rather than what its last turn did, and so that the user and
// the reviewer can edit it like any other part of the change. Merging writes
// it into the commit and removes it, so it never reaches the default branch.
const commitMessageFile = ".tps-commit-message"

// setCommitMessageL records what the file says, which is also whether the
// work is ready to merge: the dashboard offers the merge while it says anything.
// A task is named after the commit it is to become, whatever it was called
// before, so the board and the git log agree. A task its agent carries on in
// after the merge became its commit already: what is written there is for the
// task the new work goes to (see endAfterMerge).
func (t *Task) setCommitMessageL(message string) {
	if message != t.info.CommitMessage && !t.info.AfterMerge {
		t.info.CommitMessage = message
		if summary, _, _ := strings.Cut(message, "\n"); strings.TrimSpace(summary) != "" {
			t.info.Title = strings.TrimSpace(summary)
			if t.info.Scheduled != "" {
				t.info.Title = runMark + t.info.Title
			}
			t.pubL("title", t.info.Title)
		}
		t.p.m.saveL()
		t.pubL("commitMessage", orNil(message))
	}
}

// commitMessage reads what the workspace proposes as its commit message.
func (t *Task) commitMessage() string {
	data, err := os.ReadFile(filepath.Join(t.repoDir(), commitMessageFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// freshen moves a workspace that holds no work onto the latest default
// branch, so that a task that waited in Plan starts from the code as it is now
// rather than as it was when it was first opened. Work is moved by a rebase
// only (see Rebase). A workspace the branch has not moved past since — the
// one made as the plan was opened, as a rule — is left as it is, without
// staging the whole tree to look for work.
func (t *Task) freshen() error {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	repo := t.repoDir()
	if err := fetchHost(repo); err != nil {
		return err
	}
	if gitOK(repo, "merge-base", "--is-ancestor", t.upstream(), "HEAD") {
		return nil
	}
	changes, err := t.changes()
	if err != nil || len(changes) > 0 {
		return err
	}
	return gitSeq(repo,
		[]string{"reset", "--quiet", "--hard", t.upstream()},
		[]string{"update-ref", "refs/heads/" + stepsBranch, "HEAD"})
}

// leavePlan is the last dashboard leaving a task in Plan. The task is named
// after what the plan says; one with nothing in it at all — no plan, no
// title, nothing changed — is thrown away, and one whose workspace holds work
// is the human's from here on.
func (t *Task) leavePlan() {
	t.cloneMu.Lock()
	changes, err := t.changes()
	t.cloneMu.Unlock()
	if err != nil {
		logf("%s: changes: %v", t.key(), err)
		return
	}
	t.lock()
	if t.info.Phase != PhasePlan || t.viewers > 0 {
		t.unlock()
		return
	}
	if t.info.Description == "" && t.info.Deferred == "" && t.info.Title == "" && len(changes) == 0 {
		t.unlock()
		_ = t.Delete()
		return
	}
	t.ensureTitleL(t.info.Description)
	if len(changes) > 0 {
		t.setPhaseL(PhaseHuman)
	}
	t.unlock()
}

// shelve drops the workspace of a plan nobody looks at any longer, once its
// container idles: the workspace is made afresh when the task is next opened.
// One that holds work is kept.
func (t *Task) shelve() {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	changes, err := t.changes()
	t.lock()
	idle := t.info.Phase == PhasePlan && t.viewers == 0
	t.unlock()
	if err != nil || !idle || len(changes) > 0 {
		t.down()
		return
	}
	if err := t.dropWorkspace(); err != nil {
		logf("%s: dropping the workspace: %v", t.key(), err)
	}
}

// Merge queues the task for merging (see queue.go), its test skipped where
// skipTests says so. A message given here — the user's, from the merge dialog
// — goes into the workspace's file, so that it is still the one place the
// message lives: what the dashboard shows, and what an agent sent in to
// resolve conflicts reads and amends (see commitMessageFile).
func (t *Task) Merge(message string, skipTests bool) error {
	if t.p.info.Scratch {
		return errors.New("A scratch task has nothing to merge into: turn it into a project first")
	}
	if !t.hasWorkspace() {
		return errNoWorkspace
	}
	if t.phase() == PhaseMerging {
		return nil
	}
	t.stopAgent()
	if message = strings.TrimSpace(message); message != "" {
		if err := os.WriteFile(filepath.Join(t.repoDir(), commitMessageFile), []byte(message+"\n"), 0o644); err != nil {
			return err
		}
	}
	t.lock()
	defer t.unlock()
	t.info.SkipTests = skipTests
	t.setPhaseL(PhaseMerging)
	return nil
}

// landingTree is the tree of the work as it would be committed: everything in
// the workspace but the file that speaks to TPS (see commitMessageFile).
func landingTree(repo string) (string, error) {
	return stagedGit(repo, []string{"rm", "--cached", "--quiet", "--ignore-unmatch", "--", commitMessageFile}, []string{"write-tree"})
}

// hasWork: the workspace holds work to land.
func hasWork(repo string) (bool, error) {
	tree, err := landingTree(repo)
	if err != nil {
		return false, err
	}
	base, err := git(repo, "rev-parse", "HEAD^{tree}")
	return base != tree, err
}

// landed finishes a task whose work is on the branch now, as commit (or that
// had none to put there): it is Done, and its workspace is gone. An agent that
// ended on merge-and-continue is sent back in, now or once the deploy that
// follows the merge is through (see carryOn).
func (t *Task) landed(note, commit string) {
	t.note(note)
	// Before the task is Done, so it never sits there with a workspace someone
	// could still open or merge a second time.
	t.cloneMu.Lock()
	if err := t.dropWorkspace(); err != nil {
		logf("%s: dropping the workspace: %v", t.key(), err)
	}
	t.cloneMu.Unlock()
	t.lock()
	t.info.CommitMessage = ""
	t.info.ReviewLoop = 0
	t.setReviewL("")
	// The workspace these were about is gone; a task picked up after the merge
	// gets a fresh clone, and hears about that instead.
	for _, key := range []string{"rebase", "reopened", "container", "stopped", "restart"} {
		t.dropPendingL(key)
	}
	t.queueL("merged", mergedPrompt(t.p.defaultBranch))
	t.setPhaseL(PhaseMerged)
	carryOn := t.info.Continue && commit != ""
	t.info.Continue, t.info.AfterMerge = false, false
	deploys := carryOn && t.p.deploy != "" && t.p.info.AutoDeploy
	if deploys {
		t.info.AwaitDeploy = commit
	}
	t.p.m.saveL()
	t.unlock()
	t.park()
	if carryOn && !deploys {
		t.carryOn("the work is merged, and not deployed: this project does not deploy after every merge.")
	}
}

// carryOn sends the agent back into its merged task, as it asked, told how
// the merge went: in a fresh clone of the branch, and what it changes there
// becomes a task of its own (see endAfterMerge).
func (t *Task) carryOn(outcome string) {
	t.lock()
	t.info.AwaitDeploy = ""
	t.info.AfterMerge = true
	t.dropPendingL("merged") // that it is not in a follow-up is what the prompt says
	t.p.m.saveL()
	t.unlock()
	t.note("The agent asked to carry on after the merge; sending it back in")
	t.kick(continuePrompt(t.p.defaultBranch, outcome))
}

// endAfterMerge ends a run of an agent sent back into its merged task (see
// carryOn). One that changed nothing — a check of the work where it runs, say —
// leaves the task merged, or with the user where the agent is blocked. One
// that changed something carries on in a fork, named after its commit message
// like any task, with the conversation and that work, which takes the status
// from there; the merged task stays as it was.
func (t *Task) endAfterMerge(status, summary string, work bool) {
	t.mark("Agent", summary)
	if work {
		f, err := t.continueIn()
		if err != nil {
			t.noteErr("Carrying the work on in a task of its own failed", err)
			t.lock()
			t.setPhaseL(PhaseHuman)
			t.unlock()
			return
		}
		message := f.commitMessage()
		f.lock()
		f.info.Scheduled, f.info.RunAt = t.info.Scheduled, t.info.RunAt // a run's work goes on unwatched
		f.setCommitMessageL(message)
		f.publishL()
		f.info.Continue = status == turnMergeAndContinue
		to := AnswerHuman
		if status == turnMerge || status == turnMergeAndContinue {
			to = f.handsOnL()
		}
		f.unlock()
		f.handOn(to)
	} else if status == turnBlocked {
		t.lock()
		t.setPhaseL(PhaseHuman)
		t.unlock()
		return
	}
	t.cloneMu.Lock()
	if err := t.dropWorkspace(); err != nil {
		logf("%s: dropping the workspace: %v", t.key(), err)
	}
	t.cloneMu.Unlock()
	t.lock()
	t.info.AfterMerge = false
	t.setPhaseL(PhaseMerged)
	t.unlock()
	t.park()
}

// continueIn forks the task at its last save point: the conversation, and the
// steps since it was picked up after the merge.
func (t *Task) continueIn() (*Task, error) {
	log, points, err := t.chatPoints()
	if err != nil {
		return nil, err
	}
	last := points[len(points)-1]
	tid, err := t.fork(last, &last, log, "")
	t.lock()
	defer t.unlock()
	return t.p.tasks[tid], err
}

// handsOnL is where work the agent reports ready goes, as the task is set: to
// a review, which costs a turn of its own, so a task with no room left for
// one goes to the user; to the merge, which costs nothing; or to the user.
func (t *Task) handsOnL() Answer {
	switch {
	case t.onReadyL() == AnswerReview && !t.overBudgetL():
		return AnswerReview
	case t.onReadyL() == AnswerMerge:
		return AnswerMerge
	}
	return AnswerHuman
}

// handOn sends ready work where handsOnL says.
func (t *Task) handOn(to Answer) {
	switch to {
	case AnswerReview:
		t.beginReview()
	case AnswerMerge:
		t.note("The agent reports the task is ready; merging")
		_ = t.Merge("", false)
	}
}

// Rebase brings the workspace onto the latest default branch without merging
// anything into it: the replay a merge starts with (see replant), and nothing
// after it but the step that commits the merge. Conflicts send the agent in
// to resolve them instead, the step its turn ends on being the merge; either
// way it is told what happened to its workspace (see queueL).
func (t *Task) Rebase() error {
	t.lock()
	waiting := t.waitingL()
	t.unlock()
	if waiting {
		return errors.New("The task waits for other tasks; it is rebased once they are done")
	}
	conflicts, err := t.rebase()
	if conflicts {
		t.kick("") // what to resolve is in the notes rebase left
	}
	return err
}

// rebase is Rebase up to sending the agent in, and reports whether there are
// conflicts for it to resolve: the task is the agent's then, and the notes
// waiting for it say what to do.
func (t *Task) rebase() (bool, error) {
	defer t.p.m.work()()
	repo := t.repoDir()
	if !t.hasWorkspace() {
		return false, errNoWorkspace
	}
	_ = t.StopAgent()
	branch := t.p.defaultBranch
	conflicts, doubled, err := t.replant(repo)
	if err != nil {
		t.noteErr("Rebase failed", err)
		return false, err
	}
	go t.refreshChanges()
	if len(conflicts) == 0 {
		t.queue("rebase", replantedPrompt(branch, doubled))
		t.mark("Rebase", "onto the latest "+branch)
		t.note("Rebased onto the latest " + branch + " ✔")
		return false, nil
	}
	t.queue("rebase", replantedPrompt(branch, doubled)+"\n\n"+conflictsPrompt(conflicts))
	t.note(fmt.Sprintf("Rebased onto the latest %s; conflicts in %s, sending the agent in to resolve them", branch, strings.Join(conflicts, ", ")))
	t.lock()
	// The agent's already, so that the merge is not made a step of the
	// user's on the way in (see kick): its resolution is the step.
	t.setPhaseL(PhaseAgent)
	t.unlock()
	return true, nil
}

// --- the work, and the latest branch ---
//
// A task's work is what its tree differs from its base in: the commit of the
// branch its steps last took in, uncommitted changes and untracked files
// included. It is brought up to date by merging the latest branch into its
// steps (replant). No git operation stays in progress afterwards: conflicts
// are markers in the files, and the work is uncommitted on top of the branch
// again, whatever the outcome.

// stagedGit runs git commands on the whole tree staged in a copy of the index
// (unchanged files then need no rehashing), so the real one stays untouched:
// the tree's every difference from a commit, untracked files included, is a
// `diff --cached` away. It answers the last command's output. The index is
// refreshed first: one written in the same second as the files it lists — a
// fresh clone's — cannot vouch for any of them, and a copy of it would have
// every file of the tree read again, each time.
func stagedGit(repo string, cmds ...[]string) (string, error) {
	_, _ = git(repo, "update-index", "-q", "--refresh") // a lock held elsewhere only costs the rehash
	// Outside the task's own directory: a changes overview runs in the
	// background (see refreshChanges) and can still be writing this while the
	// task is being parked or thrown away, which would put a file back in a
	// directory something else is busy emptying. Git wants no particular
	// place for an index it is handed, and none or a whole one there.
	f, err := os.CreateTemp("", "tps-index-*")
	if err != nil {
		return "", err
	}
	f.Close()
	copied := f.Name()
	os.Remove(copied)
	defer os.Remove(copied)
	index := filepath.Join(repo, ".git", "index")
	if data, err := os.ReadFile(index); err == nil {
		_ = os.WriteFile(copied, data, 0o644)
		// With the original's time: git trusts what the index says of a file
		// changed no later than the index itself, and a copy made now would
		// pass off a same-size edit made in the second of the clone as none.
		if st, err := os.Stat(index); err == nil {
			_ = os.Chtimes(copied, st.ModTime(), st.ModTime())
		}
	}
	env := []string{"GIT_INDEX_FILE=" + copied}
	out := ""
	for _, args := range append([][]string{{"add", "-A"}}, cmds...) {
		if out, err = gitEnv(repo, env, args...); err != nil {
			return "", err
		}
	}
	return out, nil
}

// upstream is the default branch as a workspace has it from the host.
func (t *Task) upstream() string { return hostRemote + "/" + t.p.defaultBranch }

// base is the commit of the default branch that the work up to a commit —
// HEAD's, a step's — stands on.
func (t *Task) base(repo, of string) (string, error) {
	return git(repo, "merge-base", of, t.upstream())
}

// plant merges the latest default branch into the last step, in the working
// tree — which holds nothing that step does not — and leaves the result there
// as uncommitted work on the branch's tip: nothing staged, no merge in
// progress. What did not merge cleanly is returned: files left with conflict
// markers, or — deleted on one side and changed on the other — as the side
// that changed them had them. A merge that never started leaves HEAD where it
// was.
func (t *Task) plant(repo string) ([]string, error) {
	branch := t.p.defaultBranch
	if _, err := git(repo, "checkout", "--quiet", "--force", "--detach", stepsBranch); err != nil {
		return nil, err
	}
	var conflicts []string
	onto := t.upstream()
	_, err := git(repo, "merge", "--quiet", "--no-commit", onto)
	if out, _ := git(repo, "diff", "--name-only", "-z", "--diff-filter=U"); out != "" {
		conflicts, err = splitNul(out), nil
	}
	if err != nil {
		onto = branch // the merge never started: back where HEAD was, the branch not having moved
	}
	if err := gitSeq(repo, []string{"merge", "--quit"}, []string{"symbolic-ref", "HEAD", "refs/heads/" + branch}, []string{"reset", "--quiet", onto}); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return conflicts, nil
}

// replant brings the workspace onto the latest default branch: what the tree
// holds is made a step first, and the branch is merged into the steps (see
// plant). The merge is left uncommitted, for whoever finishes it: Rebase, or
// the agent resolving its conflicts, whose step the merge becomes (see step);
// a merge commits it to the branch instead. Besides the files that conflict,
// it answers those that merged cleanly but were changed the same way on both
// sides (see doubledFiles), which are as much the agent's to look at.
func (t *Task) replant(repo string) (conflicts, doubled []string, err error) {
	if err := fetchHost(repo); err != nil {
		return nil, nil, err
	}
	if err := t.mark("Human", ""); err != nil {
		return nil, nil, err
	}
	target, err := git(repo, "rev-parse", t.upstream())
	if err != nil {
		return nil, nil, err
	}
	base, _ := t.base(repo, "HEAD")
	if base == target {
		return nil, nil, nil // the branch has nothing this workspace lacks
	}
	doubled = doubledFiles(repo, base, stepsBranch, target)
	if conflicts, err = t.plant(repo); err != nil {
		return nil, nil, err
	}
	doubled = slices.DeleteFunc(doubled, func(f string) bool { return slices.Contains(conflicts, f) })
	return conflicts, doubled, nil
}

// doubledFiles are the files to which both the work (base..ours) and the
// branch it goes onto (base..theirs) added the same line: one change made
// twice, which git merges without a word, and which may be wrong all the
// same — two tasks bumping the version to the same number, one of which was
// deployed already. Lines without a letter or a digit in them say nothing.
func doubledFiles(repo, base, ours, theirs string) []string {
	mine := addedLines(repo, base, ours)
	if len(mine) == 0 {
		return nil
	}
	var files []string
	for file, lines := range addedLines(repo, base, theirs) {
		for line := range lines {
			if mine[file][line] {
				files = append(files, file)
				break
			}
		}
	}
	slices.Sort(files)
	return files
}

// addedLines are the lines a diff adds, by file, trimmed.
func addedLines(repo, from, to string) map[string]map[string]bool {
	out, _ := git(repo, "diff", "--no-color", "--no-renames", "--unified=0", from, to)
	added := map[string]map[string]bool{}
	file := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "+") && file != "/dev/null":
			text := strings.TrimSpace(line[1:])
			if strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
				continue
			}
			if added[file] == nil {
				added[file] = map[string]bool{}
			}
			added[file][text] = true
		}
	}
	return added
}

// markedFiles are the files in the workspace that still hold conflict markers
// from a replay — the closing one, which names the branch plant merged in —
// what a merge stops on.
func (t *Task) markedFiles(repo string) ([]string, error) {
	base, err := t.base(repo, "HEAD")
	if err != nil {
		return nil, err
	}
	marker := "^>>>>>>> " + regexp.QuoteMeta(t.upstream())
	out, err := stagedGit(repo, []string{"diff", "--cached", "--name-only", "-z", "-E", "-G" + marker, base})
	if err != nil || out == "" {
		return nil, err
	}
	return splitNul(out), nil
}

// --- the work as a bundle ---
//
// A parked task keeps its work as a git bundle of its steps: the commits of
// stepsBranch that the branch it grew from does not have, the tree as it was
// left being made a step first. Only those commits are in it; the rest of
// their history is the project repo's, which every clone starts from, so
// fetching the bundle into a fresh clone makes the steps again, commit for
// commit — the ones save points name included (see revert).

// workRef is the ref a bundle holds its work under: git bundles refs, not
// commits.
const workRef = "refs/tps/work"

// saveBundle writes the steps up to tip, from its base on, to file — or
// removes the file when there are none.
func (t *Task) saveBundle(repo, tip, file string) error {
	base, err := t.base(repo, tip)
	if err != nil {
		return err
	}
	if tip == base {
		os.Remove(file)
		return nil
	}
	if _, err := git(repo, "update-ref", workRef, tip); err != nil {
		return err
	}
	defer git(repo, "update-ref", "-d", workRef)
	_, err = git(repo, "bundle", "create", "--quiet", file, workRef, "^"+base)
	return err
}

// unbundle puts the bundle's steps into the workspace, the tree at the last
// of them (see checkoutStep), and removes the bundle.
func (t *Task) unbundle(repo string) error {
	if _, err := git(repo, "fetch", "--quiet", t.bundleFile(), workRef); err != nil {
		return err
	}
	tip, err := git(repo, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if err := t.checkoutStep(repo, tip); err != nil {
		return err
	}
	t.bundled = nil
	return os.Remove(t.bundleFile())
}

// bundleChanges lists what a parked task's bundle changes, read in a scratch
// clone that borrows the project repo's objects: once, the bundle staying as
// it is for as long as the task is parked. The caller holds cloneMu.
func (t *Task) bundleChanges() ([]change, error) {
	if t.bundled != nil {
		return t.bundled, nil
	}
	tmp, err := os.MkdirTemp("", "tps-bundle-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := gitSeq(tmp, []string{"clone", "--quiet", "--bare", "--shared", t.p.dir(), tmp}, []string{"fetch", "--quiet", t.bundleFile(), workRef}); err != nil {
		return nil, err
	}
	base, err := git(tmp, "merge-base", "FETCH_HEAD", t.p.defaultBranch)
	if err != nil {
		return nil, err
	}
	out, err := git(tmp, "diff", "--numstat", "--no-renames", "-z", base, "FETCH_HEAD")
	if err != nil {
		return nil, err
	}
	t.bundled = parseNumstat(out)
	return t.bundled, nil
}

// --- parked tasks: put away, and picked up again ---

// archived is what a parked task keeps besides its bundle, compressed into
// one archive: claude's state, the chat log, the files the user attached, and
// the services' last output.
var archived = []string{"claude", "chat.jsonl", "uploads", "services"}

// park puts a finished or muted task's data away, so that a task nobody looks
// at costs as little disk as it can: the workspace goes — with its work kept
// as a bundle, unless the task was merged and its work is on the branch — and
// what the task keeps is compressed (see archived). Picking the task up
// undoes it (see ensureWorkspace). Done when a task finishes or is muted, and
// at startup for those still as they were (see parkAll).
func (t *Task) park() {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	phase := t.phase()
	if phase != PhaseMerged && phase != PhaseClosed && phase != PhaseMuted {
		return
	}
	if t.hasWorkspace() {
		if phase != PhaseMerged {
			repo := t.repoDir()
			_, err := git(repo, "fetch", "--quiet", hostRemote)
			var tip string
			if err == nil {
				err = t.mark("Human", "") // the tree as it is left is a point to come back to
			}
			if err == nil {
				tip, err = lastStep(repo)
			}
			if err == nil {
				err = t.saveBundle(repo, tip, t.bundleFile())
			}
			if err != nil {
				t.noteErr("Keeping the work as a bundle failed", err) // the workspace stays: it is all there is of the work
				return
			}
			if phase == PhaseClosed {
				// A scratch task has no branch to be off of, and closing is the only way it ends.
				closed, off := "Closed without merging", ", off "+t.p.defaultBranch+","
				if t.p.info.Scratch {
					closed, off = "Closed", ""
				}
				if exists(t.bundleFile()) {
					t.note(closed + ": the work is kept" + off + " and comes back as it was when the task is picked up")
				} else {
					t.note(closed + "; there was no work to keep")
				}
			}
		}
		if err := t.dropWorkspace(); err != nil {
			logf("%s: dropping the workspace: %v", t.key(), err)
		}
	}
	if err := t.archive(); err != nil {
		logf("%s: compressing the task's data: %v", t.key(), err)
	}
	go t.refreshChanges() // the bundle's, from here on
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
// none: a parked task's data comes out of its archive, the branch is cloned
// afresh, and the bundle a closed or muted task kept puts its work back as it
// was (see unbundle).
func (t *Task) ensureWorkspace() error {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	if err := t.unarchive(); err != nil {
		return err
	}
	if err := t.clone(); err != nil {
		return err
	}
	if exists(t.bundleFile()) {
		if err := t.unbundle(t.repoDir()); err != nil {
			return err
		}
		t.note("The task's work was put back as it was, save points and all")
		t.queue("reopened", reopenedPrompt(t.p.defaultBranch))
	}
	return nil
}

// dropWorkspace throws the clone away, container first: what the task knows
// stays — claude's own state and the chat log live beside the clone, not in
// it — and picking the task up again clones the branch afresh, the way
// leaving Plan does. The caller holds cloneMu.
func (t *Task) dropWorkspace() error {
	t.down()
	// Also one the daemon does not know of — a job's, left running from before
	// a restart (see adoptL): found running, it would be reused (see
	// ensureContainer), its /work the directory removed here.
	_ = rmWorkspace(t.containerName(), nestDir(t.dir()))
	if err := rmTree(t.repoDir()); err != nil {
		// The clone is out of the way regardless, so the task goes on: what could
		// not be unlinked costs disk, which only the human can free.
		t.note("Workspace not fully removed: " + err.Error())
	}
	t.lock()
	defer t.unlock()
	t.pubL("changes", nil)
	t.publishL() // the workspace is gone
	return nil
}

// notePickedUp says that a finished task is being worked in again.
func (t *Task) notePickedUp() {
	t.note("Picked up again: the workspace is a fresh clone of " + t.p.defaultBranch)
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
	t.notePickedUp()
	if err := t.ensureWorkspace(); err != nil {
		t.noteErr("Workspace failed", err)
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
// it matches what would be started now. A job's is not: what it was running
// went with the daemon (see jobs.go).
func (t *Task) adoptL() {
	if !t.hasWorkspace() || t.job() {
		return
	}
	cf := t.containerfile()
	tag := imageTag(cf)
	services, caches := t.declarations(cf)
	if c := runningContainer(t.containerName(), containerConfig(tag, toolboxDir(), caches, t.mountsL())); c != nil {
		if _, err := nestFor(t.containerName(), nestDir(t.dir())); err != nil {
			logf("%s: docker socket: %v", t.key(), err)
		}
		t.container, t.lastTag = c, tag
		t.setDeclaredL(services)
		t.status = StatusUp
		t.syncServicesL(false) // what was left running rides on
		// A code-server still running in it is kept: the dashboards that showed
		// it are about to reconnect. It goes with the container once that idles.
		if codeAlive(c.CodePort) {
			t.codeUp, t.codeStart = true, time.Now().UnixMilli()
		}
	}
}

// wipe throws away everything the task has on disk and in podman: its agent,
// its container and whatever that made, its workspace and its data.
func (t *Task) wipe() {
	t.stopAgent()
	t.down()
	nestPurge(t.containerName(), nestDir(t.dir()))
	_ = rmContainer(t.containerName()) // also one the daemon never knew about
	t.cloneMu.Lock()                   // see refreshChanges
	if err := rmTree(t.dir()); err != nil {
		logf("%s: removing the task's files: %v", t.key(), err)
	}
	t.cloneMu.Unlock()
}

// dropContainer removes the task's container, and the sub-containers it
// started, socket and all (see nestDown).
func (t *Task) dropContainer() {
	nestDown(t.containerName(), nestDir(t.dir()))
	_ = rmContainer(t.containerName())
}

// dropTaskL takes the task off its project: the board, the queue, and the
// waits of the tasks that waited for it.
func (t *Task) dropTaskL() {
	t.p.dequeueL(t)
	delete(t.p.info.Tasks, t.tid)
	delete(t.p.tasks, t.tid)
	t.p.m.hub.Set([]string{"projects", t.p.pid, "tasks", t.tid}, nil)
	t.p.m.hub.SetChat(t.key(), nil)
	t.p.touchL()
	t.p.m.saveL()
	t.p.endWaitsL() // a task gone is no longer something to wait for
}

// Discard: back to plan, all work is thrown away (the UI asks for confirmation).
func (t *Task) Discard() error {
	defer t.p.m.work()()
	t.wipe()
	t.lock()
	defer t.unlock()
	t.info.Started = false
	t.setCommitMessageL("")
	t.info.ReviewLoop = 0
	t.setReviewL("")
	t.info.Pending = nil // nothing of the old workspace is left to tell
	t.p.m.hub.SetChat(t.key(), nil)
	t.pubL("changes", nil)
	t.setPhaseL(PhasePlan)
	return nil
}

func (t *Task) Delete() error {
	defer t.p.m.work()()
	t.wipe()
	t.lock()
	defer t.unlock()
	t.dropTaskL()
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
// started from, untracked files included (see stagedGit) — or, for a parked
// task, what its bundle holds.
func (t *Task) changes() ([]change, error) {
	repo := t.repoDir()
	if !t.hasWorkspace() {
		if !exists(t.bundleFile()) {
			return nil, nil
		}
		return t.bundleChanges()
	}
	base, err := t.base(repo, "HEAD")
	if err != nil {
		return nil, err
	}
	out, err := stagedGit(repo, []string{"diff", "--cached", "--numstat", "--no-renames", "-z", base})
	if err != nil {
		return nil, err
	}
	return parseNumstat(out), nil
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
	// The workspace holds still until the overview is out: one dropped
	// meanwhile would leave it published after the workspace was gone.
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	changes, err := t.changes()
	message := ""
	if t.hasWorkspace() {
		message = t.commitMessage()
	}
	t.lock()
	defer t.unlock()
	t.refreshing = false
	t.setCommitMessageL(message)
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
	t.cloneMu.Lock() // as refreshChanges does
	defer t.cloneMu.Unlock()
	repo := t.repoDir()
	behind := 0
	if t.hasWorkspace() {
		branch := t.p.defaultBranch
		if base, err := t.base(repo, "HEAD"); err == nil {
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
	t.pubL("behind", orNil(behind))
}

// --- workspace (clone + container) ---

// clone makes the workspace, unless it is there already, and gives it the
// project checkout's remotes (see matchRemotes). The caller holds cloneMu.
func (t *Task) clone() error {
	if t.hasWorkspace() {
		return matchRemotes(t.repoDir(), t.p.dir())
	}
	// Something is in its place that is not a clone (see hasWorkspace): it is
	// no workspace to anyone, and the path has to be free for git.
	if exists(t.repoDir()) {
		t.note("The workspace was not a git clone; making a fresh one")
		if err := rmTree(t.repoDir()); err != nil {
			return err
		}
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
	if err := matchRemotes(tmp, t.p.dir()); err != nil {
		return err
	}
	if err := showSteps(tmp, t.p.defaultBranch); err != nil {
		return err
	}
	fetchDeployed(tmp)
	t.seed(tmp)
	return os.Rename(tmp, t.repoDir())
}

// hostRemote is a workspace's remote for the project's checkout on the host,
// which TPS fetches the default branch and deployedRef from.
const hostRemote = "host"

// matchRemotes gives a workspace the remotes of the project's checkout, so
// that 'origin' in a task or a deploy is the user's own origin; the checkout
// itself is hostRemote. A fresh clone (or one an older TPS made) has the
// checkout as origin still, and has it renamed first.
func matchRemotes(repo, project string) error {
	if !gitOK(repo, "remote", "get-url", hostRemote) {
		if _, err := git(repo, "remote", "rename", "origin", hostRemote); err != nil {
			return err
		}
	}
	remotes := func(dir string) string {
		out, _ := git(dir, "config", "--local", "--get-regexp", `^remote\.`) // none: exit code 1
		var keep []string
		for _, line := range strings.Split(out, "\n") {
			if line != "" && !strings.HasPrefix(line, "remote."+hostRemote+".") {
				keep = append(keep, line)
			}
		}
		return strings.Join(keep, "\n")
	}
	want := remotes(project)
	if remotes(repo) == want {
		return nil
	}
	names, err := git(repo, "remote")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(names) {
		if name != hostRemote {
			if _, err := git(repo, "remote", "remove", name); err != nil {
				return err
			}
		}
	}
	for _, line := range strings.Split(want, "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			if _, err := git(repo, "config", "--add", key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// showSteps has VS Code's Source Control Graph show the steps next to the
// branch: the graph shows a branch's "base" beside it, which has to be a
// remote branch, so stepsBranch gets one — tps/steps, a symbolic ref to it.
// The branch is made at the base already: were it missing, VS Code would
// find the base unresolvable and put the default one in its place.
func showSteps(repo, branch string) error {
	return gitSeq(repo,
		[]string{"update-ref", "refs/heads/" + stepsBranch, "HEAD"},
		[]string{"symbolic-ref", "refs/remotes/tps/steps", "refs/heads/" + stepsBranch},
		[]string{"config", "branch." + branch + ".vscode-merge-base", "tps/steps"})
}

// setIdentity gives a clone the identity its commits are made under: TPS's own
// ones, and whatever is committed by hand in its container.
func (t *Task) setIdentity(repo string) error {
	id := t.p.m.gitIdentity()
	return gitSeq(repo, []string{"config", "user.name", id.Name}, []string{"config", "user.email", id.Email})
}

// Open is a dashboard's retry after a workspace failure: bring the workspace
// up, if there is one — a plan gets one from syncCode, a finished task has
// none any longer — and VS Code with it, if a dashboard shows the task.
func (t *Task) Open() {
	t.lock()
	t.touchL()
	t.unlock()
	if t.hasWorkspace() {
		t.bgUp()
	}
	go t.syncCode()
}

// --- VS Code: brought up by a dashboard showing the task, and down with the container ---

// syncCode starts code-server in the container (which is brought up first)
// while a dashboard shows the task. Nothing stops it but the container going
// down, which the idle sweep sees to: going back to a task finds its VS Code
// as it was left. It converges in a loop, as the container may be recycled
// while it works; a start that fails is reported once, and tried again by
// whatever next asks.
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
		wanted := t.viewers > 0 && !t.codeUp
		plan := t.info.Phase == PhasePlan && t.info.Schedule == nil
		t.unlock()
		if !wanted {
			return
		}
		// A plan gets its workspace the first time it is looked at; a finished
		// task has none any longer, and a scheduled one none ever: its runs do.
		var err error
		if plan {
			err = t.ensureWorkspace()
		}
		if err == nil && !t.hasWorkspace() {
			return
		}
		if err == nil {
			err = t.startCode()
		}
		if err != nil {
			t.lock()
			t.codeError = err.Error()
			t.publishL()
			t.unlock()
			return
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

// terminalBusy: is a terminal in the task's VS Code running something?
func (t *Task) terminalBusy() bool {
	t.lock()
	c, up := t.container, t.codeUp
	t.unlock()
	return c != nil && up && c.TerminalBusy()
}

// OpenInCode opens a file of the workspace in the task's VS Code.
func (t *Task) OpenInCode(path string) error {
	t.lock()
	c, up := t.container, t.codeUp
	t.unlock()
	if c == nil || !up {
		return errors.New("VS Code is not running")
	}
	return c.OpenInCode(path)
}

func (t *Task) bgUp() {
	go func() {
		if _, err := t.up(); err != nil {
			t.noteErr("Workspace failed", err)
		}
	}()
}

func (t *Task) up() (*Container, error) {
	t.lock()
	return shareL(t, &t.upFlight, func() (*Container, error) {
		defer t.p.m.work()()
		return t.doUp()
	})
}

func (t *Task) failUp(err error) (*Container, error) {
	t.lock()
	t.setStatusL(StatusError, err.Error()) // callers report the error
	t.unlock()
	return nil, err
}

func (t *Task) doUp() (*Container, error) {
	if !t.hasWorkspace() {
		return nil, errNoWorkspace
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
	t.setStatusL(StatusBuilding, "Building container image")
	t.unlock()
	if !toolboxInstalled() {
		t.note("Downloading code-server and claude for this host (once per TPS version)…")
	}
	toolbox, err := ensureToolbox()
	if err != nil {
		return t.failUp(err)
	}
	go t.p.m.refreshModels() // now that this host has a claude to ask
	c, err := t.start(cf, toolbox)
	imageErr := ""
	if isBuildErr(err) && cf != defaultContainerfile && !t.job() {
		// A broken Containerfile.dev (say, with conflict markers) must not lock
		// the user and the agent out of the task: the default image lets them
		// in to fix it. The tag stays that of the broken file, so the fallback
		// is kept until the file changes. Only a failed build falls back: what
		// goes wrong after it — podman refusing to run the container, a port it
		// cannot publish — is the host's, and another image would fail the same
		// way, so the agent is not sent to fix a file that is fine. A job does
		// not: it runs the branch's image or fails, and nobody fixes it in there.
		t.note("Building the image from Containerfile.dev failed; using the default image until it is fixed", err.Error())
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
		return nil, errNoWorkspace
	}
	t.lock()
	t.container, t.lastTag = c, tag
	services, _ := t.declarations(cf)
	t.setDeclaredL(services)
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
	go t.syncCode() // VS Code back up in it, if the task is shown
	return c, nil
}

// fixImageL sends the agent in to repair Containerfile.dev, but only when the
// task is nobody's: a phase with an agent in it — its own, the reviewer, a
// merge — has a claude on this workspace already, and a second one is what must
// never happen. Those hear of it anyway, the note waiting for the turn that
// comes next (see queueL); a kick underway carries it in itself.
func (t *Task) fixImageL() {
	if t.agentPhaseL() || t.sessionFlight != nil || t.finishedL() || t.waitingL() {
		return
	}
	go func() {
		t.note("Sending in the agent to fix Containerfile.dev")
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
		t.note("Building the dev container image; the first build takes a few minutes…")
	}
	// The build's output tail, shown in place of VS Code while it runs; published
	// a few times a second rather than per write, as every write resends it all.
	var out string
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				t.lock()
				t.pubL("buildLog", orNil(out))
				t.unlock()
			}
		}
	}()
	err := buildImage(tag, cf, t.repoDir(), func(s string) {
		t.lock()
		if out += s; len(out) > serviceLogMax {
			out = out[len(out)-serviceLogMax:]
		}
		t.unlock()
	})
	close(done)
	t.lock()
	out = "" // a tick that missed done publishes nothing either
	t.pubL("buildLog", nil)
	t.unlock()
	if err != nil {
		return nil, buildErr{err}
	}
	t.lock()
	t.setStatusL(StatusStarting, "Starting container")
	mounts, checkout := t.mountsL(), t.p.dir()
	t.unlock()
	_, caches := t.declarations(cf)
	return ensureContainer(containerOpts{name: t.containerName(), image: tag, toolbox: toolbox, repoDir: t.repoDir(), claudeDir: t.claudeDir(), servicesDir: t.servicesDir(), uploadsDir: t.uploadsDir(), nestDir: nestDir(t.dir()), cacheDir: t.p.cacheDir(), caches: caches, mounts: mounts, checkout: checkout})
}

// mountsL are the project's host files this task gets: those for all tasks,
// and those for the deploy where this is its task.
func (t *Task) mountsL() []Mount {
	var mounts []Mount
	for _, m := range t.p.info.Mounts {
		if m.AllTasks || t.tid == deployTid {
			mounts = append(mounts, m)
		}
	}
	return mounts
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
		// Between turns, taking the agent down with its container leaves
		// nothing half done: no failure for onSessionExit to report.
		s.stopping.Store(!s.TurnActive())
		s.Kill()
	}
	if c != nil {
		t.dropContainer() // what its services and its sub-containers were running goes with it
	}
	t.lock()
	t.container = nil
	t.codeUp = false
	t.clearServicesL()
	t.setStatusL(StatusDown, "")
	t.unlock()
}

// Reload recreates the workspace, rebuilding the image when Containerfile.dev
// changed: what the agent gets by changing the file, for a human
// who edited the file themselves. A live agent turn is left alone.
func (t *Task) Reload() error {
	t.lock()
	noWorkspace := !t.hasWorkspace()
	working := t.workingL()
	t.unlock()
	if noWorkspace {
		return errNoWorkspace
	}
	if working {
		return errors.New("The agent is working; stop it before rebuilding the container")
	}
	go func() {
		t.note("Recreating the workspace container")
		t.down()
		if _, err := t.up(); err != nil {
			t.noteErr("Workspace failed", err)
			return
		}
		t.queue("container", rebuiltPrompt)
	}()
	return nil
}

// --- the agent session ---

func (t *Task) ensureSession() (*Session, error) {
	t.lock()
	if s := t.session; s != nil {
		t.unlock()
		return s, nil
	}
	return shareL(t, &t.sessionFlight, t.startSession)
}

func (t *Task) startSession() (*Session, error) {
	c, err := t.up()
	if err != nil {
		return nil, err
	}
	t.lock()
	t.sessionReview = t.info.Phase == PhaseReview
	t.sessionBudget, t.sessionSystem, t.sessionMind = t.info.Budget, t.systemPromptL(), t.mindL(t.sessionReview)
	opts := SessionOpts{
		Container: c, Mind: t.sessionMind, System: t.systemPromptL(), Resume: t.info.Started,
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
		opts.System, opts.Review, opts.Resume = reviewSystem, true, false
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
	if opts.Resume {
		opts.Resumed = resumedCost(t.claudeDir())
	}
	var s *Session
	ready := make(chan struct{})
	opts.OnExit = func(code int, errTail string) {
		<-ready
		t.onSessionExit(s, code, errTail)
	}
	s, err = startClaude(opts)
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
	return "Claude's usage limit is reached, so this turn did not run. The task stays with the " +
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
	t.note("The usage limit should have reset; sending the agent back in")
	t.kick(limitPrompt)
}

// loginNote tells the user, in the task's own chat, what the failed turn was
// about and what to do: the task's page has the sign-in button above its
// message box, and signing in is all there is to it before sending the task
// back in.
const loginNote = noLogin + ", so this turn did not run and nothing about it is yours to fix: sign in, then " +
	"send the task back in."

// maxStatusNudges: how often in a row an agent is sent back in for a status
// line its turn left out, or one the work does not agree with, before the task
// is handed to the human anyway.
const maxStatusNudges = 2

// statusMismatch says what is wrong with the status a turn ended on, given
// whether the workspace holds work to land and a commit message: "" for nothing.
// In a scratch task nothing lands, and any status but a merge goes.
func statusMismatch(status string, work, message, scratch bool) string {
	switch {
	case status == "":
		return "Your turn did not end on a status line."
	case scratch:
		if status == turnMerge || status == turnMergeAndContinue {
			return "Your turn ended on " + status + ", but nothing is merged from a scratch task."
		}
	case status == turnDone && (work || message):
		return "Your turn ended on done, but the work holds changes or a .tps-commit-message, which done says it does not."
	case (status == turnMerge || status == turnMergeAndContinue) && !(work && message):
		return "Your turn ended on " + status + ", which needs both changes in the work and a .tps-commit-message, and one of them is missing."
	}
	return ""
}

// onTurnEnd: a claude turn finished. Account the cost, read what the agent
// ended on, move the task along.
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
	// nothing, so there is nothing to read out of how it ended and no run
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
	// the rest of this, as with any other failed turn. A status line means the
	// agent was talking about logins rather than failing on one.
	status, summary, _ := statusOf(end.Text)
	if end.Failed && end.NoLogin && status == "" {
		t.p.m.loginExpired()
		t.note(loginNote)
	}
	// The reviewer ends its turns with a review rather than a status line, and
	// everything below is about the agent's (see review.go). A review the task has
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
	// What the work holds, for the status the turn ended on to agree with,
	// whether the turn changed it, and whether the container still is what
	// Containerfile.dev says it should be.
	work, dirty, message, cf := false, false, "", ""
	if t.hasWorkspace() {
		work, _ = hasWork(t.repoDir())
		dirty, _ = t.dirtyTree(t.repoDir())
		message = t.commitMessage()
		cf = t.containerfile()
	}
	t.lock()
	if !t.agentPhaseL() { // stopped or dragged elsewhere meanwhile
		t.unlock()
		return
	}
	t.setCommitMessageL(message)
	// A container rebuilt mid-turn would take the agent down with it, so a
	// changed Containerfile.dev is acted on here, and the agent sent back in.
	if t.container != nil && cf != "" && imageTag(cf) != t.lastTag && !t.overBudgetL() {
		t.note("Containerfile.dev changed; recreating the workspace container")
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
	// nobody has to do anything about. A status line means the message was
	// the agent talking about limits, not claude reporting one.
	if end.Failed && end.Limited && status == "" && !t.overBudgetL() {
		if until, ok := t.limitWaitL(end.LimitAt); ok {
			t.note(limitNote(until))
			t.armLimitL(until)
			t.unlock()
			return
		}
		t.note("Claude reports a usage limit that is not one to wait out here; the task is yours")
	}
	t.limitWaits = 0
	// No status line, or one the work says otherwise to: send the agent back
	// in to set it right, unless the turn failed on its own (asking again
	// would only fail again) or it keeps getting it wrong. What is left of a
	// status then is the user's to sort out.
	if why := statusMismatch(status, work, message != "", t.p.info.Scratch); why != "" {
		if !end.Failed && !t.overBudgetL() && t.statusNudges < maxStatusNudges {
			t.statusNudges++
			t.unlock()
			t.note("The agent's turn ended on no status line, or a wrong one; sending it back in")
			t.kick(statusPrompt(why))
			return
		}
		status = turnBlocked
	}
	t.statusNudges = 0
	ready := (status == turnMerge || status == turnMergeAndContinue) && !end.Failed && !t.p.info.Scratch
	if t.info.AfterMerge {
		t.unlock()
		t.endAfterMerge(status, summary, work)
		return
	}
	// The latest status decides, but for the merge it goes on with: told to
	// end on merge there, an agent has not taken back carrying on after it.
	if t.info.Phase != PhaseMerging || status == turnMergeAndContinue {
		t.info.Continue = ready && status == turnMergeAndContinue
		t.p.m.saveL()
	}
	if t.info.Phase == PhaseMerging { // the agent was resolving conflicts, or fixing what failed the merge test
		if !ready {
			if t.overBudgetL() {
				t.noteBudgetL()
			}
			t.note("Merge paused: the agent does not report the work ready")
			t.endRunL(summary)
			return
		}
		t.unlock()
		t.mark("Agent", summary) // the resolution is its step, not the user's (see replant)
		t.note("The agent is done; the merge goes on")
		t.lock()
		t.merging = false
		t.p.startHeadL()
		t.unlock()
		return
	}
	// Nothing to merge, and nothing asked of anyone: a task that merges by
	// itself is finished, as it would be by a merge of nothing.
	if status == turnDone && !t.p.info.Scratch && t.mergesByItselfL() {
		t.unlock()
		t.mark("Agent", summary)
		t.stopAgent()
		t.landed("The agent reports there is nothing to merge; task done", "")
		return
	}
	// What becomes of work the agent calls ready is the task's own answer (see
	// review.go) — once, for the turn that made it so: a turn that changed
	// nothing, say an answer to a question, leaves the work where it was. A
	// scheduled run has no user to leave the work with, so its work is passed
	// on whenever its agent says so. The run's save point is made first, as
	// its step rather than the user's (see replant); a review makes one of its
	// own when it is done.
	if ready && (dirty || t.info.Scheduled != "") {
		if to := t.handsOnL(); to != AnswerHuman {
			t.unlock()
			t.mark("Agent", summary)
			t.handOn(to)
			return
		}
	}
	if t.overBudgetL() {
		if t.overrunL() {
			t.unlock()
			t.mark("Agent", summary)
			t.kick("") // the wrap-up prompt overrunL queued is the message
			return
		}
		t.noteBudgetL()
	}
	t.endRunL(summary)
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

func (t *Task) onSessionExit(s *Session, code int, errTail string) {
	t.lock()
	defer t.unlock()
	if t.session == s {
		t.session = nil
	}
	t.publishL()
	if t.info.LimitUntil > 0 {
		// Waiting out a usage limit: the agent's process is not what the task is
		// waiting for (the container may well be recycled for idling before
		// the reset), and the resume starts one of its own.
		return
	}
	if t.agentPhaseL() && !t.stopping && !s.stopping.Load() {
		// An agent that could not sign in dies before a turn of it ever runs,
		// and says so on its way out: that is the host's login rather than
		// this task, and there is nothing half-done to keep.
		if code != 0 && authGone(errTail) {
			t.p.m.loginExpired()
			t.note(loginNote)
		} else if code != 0 {
			t.note(fmt.Sprintf("The agent exited unexpectedly (%d)", code), errTail)
			// Whatever it had got to is in the tree and nowhere else; a point
			// of its own is what makes it something to come back to.
			go t.mark("Agent", "the agent exited part-way")
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
