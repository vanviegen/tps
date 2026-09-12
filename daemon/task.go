package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
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
	PhaseMerge  Phase = "merge"  // TPS is merging: committing, rebasing, or an agent resolving conflicts
	PhaseDone   Phase = "done"   // merged: its work is on the branch and its workspace is gone
	PhaseClosed Phase = "closed" // closed without merging: its work stays in its own worktree, off the branch
)

var phases = []Phase{PhasePlan, PhaseAgent, PhaseHuman, PhaseMuted, PhaseMerge, PhaseDone, PhaseClosed}

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

// TaskInfo is the persisted part of a task (in projects.json).
type TaskInfo struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Model         string   `json:"model"`
	Phase         Phase    `json:"phase"`
	Started       bool     `json:"started,omitempty"`       // a claude session exists in the task's claude dir
	CommitMessage string   `json:"commitMessage,omitempty"` // proposed by the agent, awaiting the user's merge
	Merged        bool     `json:"merged,omitempty"`        // the work was committed since the agent's last turn; it is told so when it is sent back in
	Spent         float64  `json:"spent,omitempty"`         // USD spent on agent runs so far
	Budget        *float64 `json:"budget,omitempty"`        // USD limit; the task is parked when spending reaches it
	AutoMerge     *bool    `json:"autoMerge,omitempty"`     // merge without confirmation; copied from the project's defaults at creation
	TitleAsked    bool     `json:"titleAsked,omitempty"`    // claude has been asked to name this task (once is enough)
	PhaseAt       int64    `json:"phaseAt,omitempty"`       // ms epoch of the last phase change; boards show the freshest first
	StartAfter    []string `json:"startAfter,omitempty"`    // tids this task follows: it leaves Plan by itself once they are all done
	RebaseOnly    bool     `json:"rebaseOnly,omitempty"`    // the agent in the merge phase is resolving a plain rebase, which ends there
	TmpCommit     bool     `json:"tmpCommit,omitempty"`     // the workspace holds a temporary commit with what was uncommitted (see stashInCommit)
	LimitUntil    int64    `json:"limitUntil,omitempty"`    // ms epoch the agent goes back in at, waiting out a usage limit (see armLimitL)

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
// until the task is deleted or discarded back to plan. A sibling dir holds
// the claude conversation state, another file the condensed chat log.
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
func (t *Task) containerName() string { return "tps-" + t.p.pid + "-" + t.tid }
func (t *Task) workingL() bool        { return t.session != nil && t.session.TurnActive() }
func (t *Task) touchL()               { t.lastActivity = time.Now() }

// agentPhaseL: an agent may be at work, on the task itself or on merging it.
func (t *Task) agentPhaseL() bool { return t.info.Phase == PhaseAgent || t.info.Phase == PhaseMerge }

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
	t.pubL("phase", t.info.Phase)
	t.pubL("phaseAt", t.info.PhaseAt)
	t.pubL("commitMessage", nonEmpty(t.info.CommitMessage))
	if t.info.Spent > 0 {
		t.pubL("spent", t.info.Spent)
	} else {
		t.pubL("spent", nil)
	}
	t.pubL("budget", optional(t.info.Budget))
	t.pubL("autoMerge", optional(t.info.AutoMerge))
	if len(t.info.StartAfter) > 0 {
		t.pubL("startAfter", t.info.StartAfter)
	} else {
		t.pubL("startAfter", nil)
	}
	if t.midRebase() {
		t.pubL("rebasing", true)
	} else {
		t.pubL("rebasing", nil)
	}
	if t.info.RebaseOnly {
		t.pubL("rebaseOnly", true)
	} else {
		t.pubL("rebaseOnly", nil)
	}
	// Whether there is a workspace to open at all: a task in Plan has none yet,
	// a merged one no longer, and one closed without merging kept the one it had.
	if exists(t.repoDir()) {
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
	if phase != PhaseAgent && phase != PhaseMerge { // a task leaving the agent waits for nothing
		t.clearLimitL()
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

func (t *Task) loadChat() {
	f, err := os.Open(t.chatFile())
	if err != nil {
		return
	}
	defer f.Close()
	var entries []hub.Entry
	byID := map[string]int{}
	sc := bufio.NewScanner(f)
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
	t.p.m.hub.SetChat(t.key(), entries)
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
	if auto, ok := partial["autoMerge"].(bool); ok {
		t.info.AutoMerge = &auto
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
	if err := t.ensureClone(); err != nil {
		return err
	}
	if to == "agent" {
		e := newEntry("user")
		e.Text = desc
		t.addEntry(e)
		t.kick(desc, false)
	} else {
		t.lock()
		t.setPhaseL(PhaseHuman)
		t.unlock()
		t.bgUp()
	}
	return nil
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
	t.lock()
	if t.info.Phase == PhasePlan {
		t.unlock()
		return errors.New("Assign the task to the agent first")
	}
	t.unlock()
	text, err := t.saveUploads(text, files)
	if err != nil {
		return err
	}
	t.lock()
	t.touchL()
	t.p.touchL()
	t.doneNudges = 0
	t.unlock()
	e := newEntry("user")
	e.Text = text
	t.addEntry(e)
	t.kick(text, false)
	return nil
}

func sameBudget(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// kick makes sure a claude session is running and feeds it text, in the
// background. With fresh, any current session is stopped and a new context
// is started. A task being merged stays in that phase: the agent then works
// on the merge. An agent sent back in after its work was merged (info.Merged)
// picks the conversation up where it ended, in a workspace freshly cloned from
// the branch, and is told as much: what follows becomes a patch of its own.
func (t *Task) kick(text string, fresh bool) {
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
	reopened := t.info.Merged
	wasDone := t.info.Phase == PhaseDone
	wasClosed := t.info.Phase == PhaseClosed
	if t.midRebase() { // the agent's 'merge' verdict then reruns it
		t.setPhaseL(PhaseMerge)
	} else if t.info.Phase != PhaseMerge {
		t.setPhaseL(PhaseAgent)
	}
	// A running claude has its spending cap fixed at start; a changed budget needs a new process.
	old := t.session
	restart := old != nil && (fresh || !sameBudget(t.sessionBudget, t.info.Budget))
	branch := t.p.defaultBranch
	t.unlock()
	go func() {
		if restart {
			t.stopSession(old)
		}
		if reopened {
			// The workspace is made before the container comes up on it. Straight
			// out of Done that is a clone of the branch as it is now; a task a
			// human picked up first has one already, with their work in it.
			var err error
			if wasDone {
				err = t.freshClone()
			} else {
				err = t.ensureClone()
			}
			if err != nil {
				t.failKick("workspace failed", err)
				return
			}
			if wasDone {
				t.note("picked up after the merge; the workspace is a fresh clone of " + branch)
			}
			text = continuePrompt(branch, text)
		} else if wasClosed {
			// Closed without merging: its worktree is normally still there, but
			// a task closed before it ever started has none.
			if err := t.ensureClone(); err != nil {
				t.failKick("workspace failed", err)
				return
			}
		}
		s, err := t.ensureSession(fresh)
		if err != nil {
			t.failKick("agent start failed", err)
			return
		}
		// Taken only now that there is someone to tell: a kick that never got
		// this far leaves the notes waiting for the next one.
		t.lock()
		text = t.takePendingL(text)
		t.unlock()
		s.Send(text)
		t.lock()
		if reopened { // told only now: a kick that never got this far leaves it to be said
			t.info.Merged = false
			t.p.m.saveL()
		}
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

// StopAgent hands the task to the human, interrupting any agent turn (or merge).
func (t *Task) StopAgent() error {
	t.lock()
	s := t.session
	if t.workingL() {
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
	return nil
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
		if t.midRebase() {
			t.kickRebase(false)
			return nil
		}
		return t.SendChat("Please continue working on the task.", nil)
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
		case PhaseAgent, PhaseMerge:
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

// Close puts a task away without merging it: its work stays in its own
// worktree, off the default branch, and can still be opened, merged, or
// thrown out with the task. The dashboard offers this beside merging, and
// recommends merging.
func (t *Task) Close() error {
	_ = t.StopAgent()
	if exists(t.repoDir()) {
		t.note("closed without merging: its work stays in the task's own worktree, off " + t.p.defaultBranch)
	}
	t.lock()
	defer t.unlock()
	t.info.CommitMessage = ""
	t.setPhaseL(PhaseClosed)
	return nil
}

// Merge commits the working tree as one commit, rebases it onto the latest
// default branch if that moved, and fast-forwards the project repo. The task
// sits in the merge phase meanwhile: rebase conflicts are handed to a fresh
// agent there, whose 'merge' verdict runs it again. A failure puts the task
// back with the human.
func (t *Task) Merge(message string) error {
	defer t.p.m.work()()
	repo := t.repoDir()
	if !exists(repo) {
		return errors.New("The task has no workspace to merge: it is still in Plan, or merged already")
	}
	if t.midRebase() {
		return errors.New("A rebase is still in progress in the workspace; let the agent finish it (or resolve it in VS Code) first")
	}
	_ = t.StopAgent()
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
	// A temporary commit from a rebase is not a commit of its own: it holds
	// what the working tree had, and goes back into it before this one is made.
	if err := t.undoTmpCommit(repo); err != nil {
		return err
	}
	if _, err := git(repo, "add", "-A"); err != nil {
		return err
	}
	if !gitOK(repo, "diff", "--cached", "--quiet") {
		if _, err := git(repo, "commit", "--no-verify", "-m", message); err != nil {
			return err
		}
	}
	target, handed, err := t.rebaseOnto(repo)
	if err != nil {
		return err
	}
	if handed { // an agent is resolving the conflicts; it comes back through onTurnEnd
		return nil
	}
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
	t.info.Merged = true // the agent's tree is clean because of this, not because it did nothing
	t.info.RebaseOnly = false
	// The workspace these were about is gone; a task picked up after the merge
	// gets a fresh clone, and hears about that instead (see continuePrompt).
	for _, key := range []string{"rebase", "container", "stopped", "restart"} {
		t.dropPendingL(key)
	}
	t.setPhaseL(PhaseDone)
	t.unlock()
	return nil
}

// rebaseOnto brings the workspace up to date with the default branch: it
// fetches, and replays whatever the workspace has on top of it. Conflicts are
// handed to a fresh agent (see kickRebase), which the `handed` return says —
// the caller is then done, and the work continues in onTurnEnd. Shared by the
// merge, which rebases before it fast-forwards, and by Rebase, which stops here.
func (t *Task) rebaseOnto(repo string) (target string, handed bool, err error) {
	branch := t.p.defaultBranch
	if _, err = git(repo, "fetch", "--quiet", "origin"); err != nil {
		return "", false, err
	}
	if target, err = git(repo, "rev-parse", "origin/"+branch); err != nil {
		return "", false, err
	}
	if gitOK(repo, "merge-base", "--is-ancestor", target, "HEAD") {
		return target, false, nil // the branch has nothing this workspace lacks
	}
	if _, err := git(repo, "rebase", target); err != nil {
		if !t.midRebase() {
			return "", false, fmt.Errorf("rebase onto %s failed", branch)
		}
		t.note(fmt.Sprintf("rebasing onto the latest %s hit conflicts; sending in a fresh agent to resolve them", branch))
		t.kickRebase(true)
		return target, true, nil
	}
	return target, false, nil
}

// Rebase brings the workspace onto the latest default branch without merging
// anything into it: the same replay a merge does first, and nothing after it.
// What the working tree has is put in a temporary commit so the rebase has
// something to move, and taken back out of it when the replay lands. Conflicts
// go the way a merge's do — a fresh agent resolves them, with the task in the
// merge phase meanwhile — and the agent is told what happened to its workspace
// the next time it is sent in (see queueL).
func (t *Task) Rebase() error {
	defer t.p.m.work()()
	repo := t.repoDir()
	if !exists(repo) {
		return errors.New("The task has no workspace to rebase")
	}
	if t.midRebase() {
		return errors.New("A rebase is still in progress in the workspace; let the agent finish it (or resolve it in VS Code) first")
	}
	_ = t.StopAgent()
	t.lock()
	t.info.RebaseOnly = true
	t.setPhaseL(PhaseMerge)
	t.unlock()
	err := t.rebase(repo)
	if err != nil {
		t.noteErr("rebase failed", err)
		t.lock()
		t.info.RebaseOnly = false
		if t.info.Phase == PhaseMerge {
			t.setPhaseL(PhaseHuman)
		}
		t.unlock()
	}
	go t.refreshChanges()
	return err
}

func (t *Task) rebase(repo string) error {
	if err := t.stashInCommit(repo); err != nil {
		return err
	}
	_, handed, err := t.rebaseOnto(repo)
	if err != nil {
		return err
	}
	if handed { // the agent has it; finishRebase runs when its turn ends
		return nil
	}
	return t.finishRebase(repo)
}

// finishRebase takes the working tree back out of its temporary commit and
// hands the task back to the human, with a note for the agent's next turn:
// the ground under its workspace moved while it wasn't running.
func (t *Task) finishRebase(repo string) error {
	if err := t.undoTmpCommit(repo); err != nil {
		return err
	}
	branch := t.p.defaultBranch
	t.note("rebased onto the latest " + branch + " ✔")
	t.lock()
	t.info.RebaseOnly = false
	t.queueL("rebase", rebasedPrompt(branch))
	if t.info.Phase == PhaseMerge {
		t.setPhaseL(PhaseHuman)
	}
	t.unlock()
	go t.refreshChanges()
	return nil
}

// tmpCommitSubject marks the commit a rebase parks the working tree in, so it
// can be told from the task's own work — and undone — later on.
const tmpCommitSubject = "TPS: work in progress (temporary commit for a rebase)"

// stashInCommit puts everything the workspace has in a commit of its own: a
// rebase moves commits, not working trees. Nothing to commit, nothing to do.
func (t *Task) stashInCommit(repo string) error {
	if _, err := git(repo, "add", "-A"); err != nil {
		return err
	}
	if gitOK(repo, "diff", "--cached", "--quiet") {
		return nil
	}
	if _, err := git(repo, "commit", "--no-verify", "-m", tmpCommitSubject); err != nil {
		return err
	}
	t.lock()
	defer t.unlock()
	t.info.TmpCommit = true
	t.p.m.saveL()
	return nil
}

// undoTmpCommit puts that commit back where it came from: the working tree it
// was made of, files untracked as it found them (a mixed reset, so nothing is
// left staged that wasn't). The subject is checked, so a commit the agent made
// on top of it (it is asked not to, but still) is left alone.
func (t *Task) undoTmpCommit(repo string) error {
	t.lock()
	parked := t.info.TmpCommit
	t.unlock()
	if !parked {
		return nil
	}
	if subject, _ := git(repo, "log", "-1", "--format=%s"); subject == tmpCommitSubject {
		if _, err := git(repo, "reset", "--quiet", "HEAD~1"); err != nil {
			return err
		}
	}
	t.lock()
	defer t.unlock()
	t.info.TmpCommit = false
	t.p.m.saveL()
	return nil
}

// dropWorkspace throws the clone away, container first: once its work is on
// the branch there is nothing in it the project repo doesn't have, and picking
// the task up again clones the branch afresh, the way leaving Plan does. What
// the task knows stays: claude's own state and the chat log live beside the
// clone, not in it.
func (t *Task) dropWorkspace() error {
	t.down()
	if err := rmTree(t.repoDir()); err != nil {
		// The clone is out of the way regardless, so the task goes on: what could
		// not be unlinked costs disk, which only the human can free.
		t.note("workspace not fully removed: " + err.Error())
	}
	_ = os.Remove(filepath.Join(t.dir(), "changes-index")) // it described the clone
	t.lock()
	defer t.unlock()
	t.pubL("changes", nil)
	return nil
}

// freshClone gives a task that was merged its workspace back: the branch as it
// stands now. Whatever is left of the old one goes first — the merge normally
// threw it away already, but one merged by an older TPS is still there, and it
// is not what the task is about to be picked up with.
func (t *Task) freshClone() error {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	if err := t.dropWorkspace(); err != nil {
		return err
	}
	return t.clone()
}

// kickRebase sends the agent in to finish the rebase a merge — or a plain
// rebase, which ends as soon as the replay lands — got stuck in.
func (t *Task) kickRebase(fresh bool) {
	t.lock()
	prompt := conflictPrompt(t.p.defaultBranch, t.info.CommitMessage)
	if t.info.RebaseOnly {
		prompt = rebaseConflictPrompt(t.p.defaultBranch)
	}
	t.unlock()
	t.kick(prompt, fresh)
}

func (t *Task) midRebase() bool {
	return exists(filepath.Join(t.repoDir(), ".git", "rebase-merge")) || exists(filepath.Join(t.repoDir(), ".git", "rebase-apply"))
}

// reopen picks a closed task back up. One that was merged has no workspace
// left, and gets a clone of the branch as it is now — its own work included,
// and everything that landed since; one that was closed without merging kept
// its worktree, unmerged work and all, and that is what it comes back with.
// The conversation was kept beside it either way. The workspace is made here
// for a human to work in; the agent gets the same through kick. The 'merged'
// mark stays: it is the agent that has yet to hear of the merge.
func (t *Task) reopen() error {
	defer t.p.m.work()()
	t.lock()
	merged := t.info.Phase == PhaseDone
	t.unlock()
	// Merged, whatever is left of the old workspace is not what the task should
	// come back with (the merge threw it away; one merged by an older TPS did
	// not). Closed, it is exactly that.
	clone := t.ensureClone
	if merged {
		clone = t.freshClone
	}
	if err := clone(); err != nil {
		t.noteErr("workspace failed", err)
		return err
	}
	if merged {
		t.note("picked up after the merge; the workspace is a fresh clone of " + t.p.defaultBranch)
	}
	t.lock()
	t.setPhaseL(PhaseHuman)
	t.unlock()
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
	if t.info.Phase == PhasePlan || !exists(t.repoDir()) {
		return
	}
	cf := t.containerfile()
	tag := imageTag(cf)
	if c := runningContainer(t.containerName(), containerConfig(tag, toolboxDir())); c != nil {
		if _, err := nestFor(t.containerName(), nestDir(t.dir())); err != nil {
			logf("%s: docker socket: %v", t.key(), err)
		}
		t.container, t.lastTag = c, tag
		t.setDeclaredL(containerfileServices(cf))
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
	_ = t.StopAgent()
	t.down()
	rmContainer(t.containerName()) // also one the daemon never knew about
	nestPurge(t.containerName(), nestDir(t.dir()))
	if err := rmTree(t.dir()); err != nil {
		logf("%s: discarding the workspace: %v", t.key(), err)
	}
	t.lock()
	defer t.unlock()
	t.info.Started = false
	t.info.CommitMessage = ""
	t.info.Merged = false
	t.info.RebaseOnly, t.info.TmpCommit, t.info.Pending = false, false, nil // nothing of the old workspace is left to tell
	t.p.m.hub.SetChat(t.key(), nil)
	t.pubL("changes", nil)
	t.setPhaseL(PhasePlan)
	return nil
}

func (t *Task) Delete() error {
	defer t.p.m.work()()
	_ = t.StopAgent()
	t.down()
	rmContainer(t.containerName())
	nestPurge(t.containerName(), nestDir(t.dir()))
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
// started from (or, mid-rebase, since the branch it is being replayed onto),
// untracked files included. A throwaway copy of the index (so the real one
// stays untouched, and unchanged files need no rehashing) makes git see the
// whole tree as staged.
func (t *Task) changes() ([]change, error) {
	repo := t.repoDir()
	base, err := git(repo, "merge-base", "HEAD", "origin/"+t.p.defaultBranch)
	if err != nil {
		return nil, err
	}
	index := filepath.Join(t.dir(), "changes-index")
	if data, err := os.ReadFile(filepath.Join(repo, ".git", "index")); err == nil {
		_ = os.WriteFile(index, data, 0o644)
	}
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := runCmd([]string{"git", "-C", repo, "add", "-A"}, RunOpts{Env: env}); err != nil {
		return nil, err
	}
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
	if t.refreshing || !exists(t.repoDir()) {
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
	t.pubL("changes", changes)
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
	if exists(repo) {
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

func (t *Task) ensureClone() error {
	t.cloneMu.Lock()
	defer t.cloneMu.Unlock()
	return t.clone()
}

// clone makes the workspace, unless it is there already. The caller holds cloneMu.
func (t *Task) clone() error {
	if exists(t.repoDir()) {
		return nil
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
	if _, err := git(tmp, "config", "user.name", "TPS"); err != nil {
		return err
	}
	if _, err := git(tmp, "config", "user.email", "tps@localhost"); err != nil {
		return err
	}
	return os.Rename(tmp, t.repoDir())
}

// Open is a dashboard's retry after a workspace failure: bring the workspace
// up, if there is one — a task in Plan has none yet, a merged one no longer —
// and VS Code with it, if the dashboard holds the task.
func (t *Task) Open() {
	t.lock()
	t.touchL()
	phase := t.info.Phase
	t.unlock()
	if phase != PhasePlan && exists(t.repoDir()) {
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
			// Nothing to show yet, or no longer: a plan has no workspace, a merged
			// task lost its one. The hold is left standing for when it has one.
			if phase == PhasePlan || !exists(t.repoDir()) {
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
	if !exists(t.repoDir()) {
		return nil, errors.New("The task has no workspace: it is still in Plan, or merged already")
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
	if err != nil && cf != defaultContainerfile {
		// A broken Containerfile.dev (say, with conflict markers) must not lock
		// the user and the agent out of the task: the default image lets them
		// in to fix it. The tag stays that of the broken file, so the fallback
		// is kept until the file changes.
		t.note("the container from Containerfile.dev failed; using the default image until it is fixed", err.Error())
		imageErr = err.Error()
		cf = defaultContainerfile
		c, err = t.start(cf, toolbox)
	}
	if err != nil {
		return t.failUp(err)
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

// fixImageL sends the agent in to repair Containerfile.dev, unless a kick is
// underway already (it then carries the message, see kick). Mid-rebase, the
// conflict resolution the merge was waiting for is resumed.
func (t *Task) fixImageL() {
	if t.sessionFlight != nil || t.finishedL() {
		return
	}
	rebase := t.midRebase()
	go func() {
		t.note("sending in the agent to fix Containerfile.dev")
		if rebase {
			t.kickRebase(true)
		} else {
			t.kick(fixImagePrompt, false)
		}
	}()
}

// start builds the image for this Containerfile (if needed) and starts the task's container from it.
func (t *Task) start(cf, toolbox string) (*Container, error) {
	tag := imageTag(cf)
	if !imageExists(tag) {
		t.note("building the dev container image; the first build takes a few minutes…")
	}
	if err := buildImage(tag, cf, t.repoDir(), func(string) {}); err != nil {
		return nil, err
	}
	t.lock()
	t.setStatusL(StatusStarting, "starting container")
	t.unlock()
	return ensureContainer(containerOpts{name: t.containerName(), image: tag, toolbox: toolbox, repoDir: t.repoDir(), claudeDir: t.claudeDir(), servicesDir: t.servicesDir(), uploadsDir: t.uploadsDir(), nestDir: nestDir(t.dir())})
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
		c.Rm() // its services go with it
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
	noWorkspace := t.info.Phase == PhasePlan || !exists(t.repoDir())
	working := t.workingL()
	t.unlock()
	if noWorkspace {
		return errors.New("The task has no workspace: it is still in Plan, or merged already")
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

func (t *Task) ensureSession(fresh bool) (*ChatSession, error) {
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
			f.val, f.err = t.startSession(fresh)
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

func (t *Task) startSession(fresh bool) (*ChatSession, error) {
	c, err := t.up()
	if err != nil {
		return nil, err
	}
	t.lock()
	t.sessionBudget = t.info.Budget
	opts := SessionOpts{
		Container: c, Model: t.info.Model, System: systemPrompt, Resume: t.info.Started && !fresh,
		OnEntry:   t.addEntry,
		OnUpdate:  t.updateEntry,
		OnTurnEnd: func(end TurnEnd) { go t.onTurnEnd(end) },
	}
	if t.info.Budget != nil {
		left := max(0.01, *t.info.Budget-t.info.Spent)
		opts.Budget = &left
	}
	if !t.info.Started {
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
	t.kick(limitPrompt, false)
}

// maxDoneNudges: how often in a row an agent is sent back in for the TPS-DONE
// line it forgot before the task is handed to the human anyway.
const maxDoneNudges = 2

// onTurnEnd: a claude turn finished. Account the cost, read the verdict the
// agent ended on, move the task along.
func (t *Task) onTurnEnd(end TurnEnd) {
	t.lock()
	t.touchL()
	t.p.touchL()
	if end.Cost > 0 {
		t.info.Spent = math.Round((t.info.Spent+end.Cost)*10000) / 10000
		t.p.m.saveL()
	}
	t.publishL()
	if t.workingL() { // the user already sent a follow-up
		t.unlock()
		return
	}
	t.unlock()
	go t.refreshChanges()
	t.lock()
	if !t.agentPhaseL() { // stopped or dragged elsewhere meanwhile
		t.unlock()
		return
	}
	next := ""
	if end.Done != nil {
		next = end.Done.Next
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
		t.kick(reloadedPrompt, false)
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
		t.kick(donePrompt(end.Bad), false)
		return
	}
	t.doneNudges = 0
	if t.info.Phase == PhaseMerge && t.info.RebaseOnly {
		// A plain rebase: nothing is merged at the end of it, so any verdict
		// will do — what counts is whether the replay landed.
		if t.overBudgetL() {
			t.noteBudgetL()
		}
		t.unlock()
		if t.midRebase() {
			t.note("rebase paused: it is unfinished")
			t.lock()
			t.info.RebaseOnly = false
			if t.info.Phase == PhaseMerge {
				t.setPhaseL(PhaseHuman)
			}
			t.unlock()
			return
		}
		if err := t.finishRebase(t.repoDir()); err != nil {
			t.noteErr("rebase failed", err)
			t.lock()
			t.info.RebaseOnly = false
			if t.info.Phase == PhaseMerge {
				t.setPhaseL(PhaseHuman)
			}
			t.unlock()
		}
		return
	}
	if t.info.Phase == PhaseMerge { // the agent was resolving rebase conflicts
		msg := t.info.CommitMessage
		if next != "merge" {
			if t.overBudgetL() {
				t.noteBudgetL()
			}
			t.note("merge paused: the rebase is unfinished")
			t.setPhaseL(PhaseHuman)
			t.unlock()
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
		if t.info.AutoMerge != nil && *t.info.AutoMerge {
			msg := t.info.CommitMessage
			t.unlock()
			t.note("the agent reports the task is ready; merging")
			_ = t.Merge(msg)
			return
		}
		t.note("the agent reports the task is ready to merge")
	}
	if t.overBudgetL() {
		t.noteBudgetL()
	}
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
		if code != 0 {
			t.note(fmt.Sprintf("claude exited unexpectedly (%d)", code), errTail)
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
