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

	"github.com/vanviegen/agent-manager/hub"
)

type Phase string

const (
	PhasePlan  Phase = "plan"
	PhaseAgent Phase = "agent"
	PhaseHuman Phase = "human"
	PhaseMerge Phase = "merge" // TPS is merging: committing, rebasing, or an agent resolving conflicts
	PhaseDone  Phase = "done"
)

var phases = []Phase{PhasePlan, PhaseAgent, PhaseHuman, PhaseMerge, PhaseDone}

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

// TaskInfo is the persisted part of a task (in projects.json).
type TaskInfo struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Model         string   `json:"model"`
	Phase         Phase    `json:"phase"`
	Started       bool     `json:"started,omitempty"`       // a claude session exists in the task's claude dir
	CommitMessage string   `json:"commitMessage,omitempty"` // proposed by the agent, awaiting the user's merge
	Spent         float64  `json:"spent,omitempty"`         // USD spent on agent runs so far
	Budget        *float64 `json:"budget,omitempty"`        // USD limit; the task is parked when spending reaches it
	AutoMerge     *bool    `json:"autoMerge,omitempty"`     // overrides the project setting when set
	Dependencies  []string `json:"dependencies,omitempty"`  // tids that must be merged or deleted first
	Waiting       bool     `json:"waiting,omitempty"`       // assigned to the agent, parked until the dependencies resolve
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
	live         bool
	checkingLive bool
	refreshing   bool // a changes overview is being computed

	session       *ChatSession
	sessionFlight *flight[*ChatSession]
	sessionBudget *float64 // the budget setting the running claude was started under
	upFlight      *flight[*Container]
	lastTag       string
	stopping      bool
	chatMu        sync.Mutex

	run     *runSession
	runCmd  []string // the Containerfile's CMD, known along with the container
	runCode *int     // the last run's exit code, until the next one starts
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
	t.pubL("commitMessage", nonEmpty(t.info.CommitMessage))
	if t.info.Spent > 0 {
		t.pubL("spent", t.info.Spent)
	} else {
		t.pubL("spent", nil)
	}
	t.pubL("budget", optional(t.info.Budget))
	t.pubL("autoMerge", optional(t.info.AutoMerge))
	if len(t.info.Dependencies) > 0 {
		t.pubL("dependencies", t.info.Dependencies)
	} else {
		t.pubL("dependencies", nil)
	}
	if t.info.Waiting {
		t.pubL("waiting", true)
	} else {
		t.pubL("waiting", nil)
	}
	t.pubL("status", t.status)
	t.pubL("statusDetail", t.statusDetail)
	t.pubL("working", t.workingL())
	t.pubL("live", t.live)
	if t.status == StatusUp && t.container != nil {
		t.pubL("appPort", t.container.AppPort)
		t.pubL("codePort", t.container.CodePort)
		t.pubL("runCmd", nonEmpty(cmdDisplay(t.runCmd)))
	} else {
		t.pubL("appPort", nil)
		t.pubL("codePort", nil)
		t.pubL("runCmd", nil)
	}
	switch {
	case t.run != nil:
		t.pubL("run", map[string]any{"status": "running"})
	case t.runCode != nil:
		t.pubL("run", map[string]any{"status": "exited", "code": *t.runCode})
	default:
		t.pubL("run", nil)
	}
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
		t.setLiveL(false)
	}
	t.publishL()
}

func (t *Task) setPhaseL(phase Phase) {
	t.info.Phase = phase
	if phase != PhaseAgent {
		t.info.Waiting = false
	}
	t.p.touchL()
	t.p.m.saveL()
	t.publishL()
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
	if title, ok := partial["title"].(string); ok && strings.TrimSpace(title) != "" {
		t.info.Title = strings.TrimSpace(title)
	}
	if desc, ok := partial["description"].(string); ok && t.info.Phase == PhasePlan { // what the agent was given stays
		t.info.Description = desc
	}
	if model, ok := partial["model"].(string); ok {
		t.info.Model = model
	}
	if auto, ok := partial["autoMerge"].(bool); ok {
		t.info.AutoMerge = &auto
	}
	if raw, ok := partial["budget"]; ok {
		t.info.Budget = nil
		var budget float64
		switch v := raw.(type) {
		case float64:
			budget = v
		case string:
			fmt.Sscanf(strings.TrimSpace(v), "%g", &budget)
		}
		if budget > 0 && !math.IsInf(budget, 0) {
			t.info.Budget = &budget
		}
	}
	if raw, ok := partial["dependencies"].([]any); ok {
		var deps []string
		seen := map[string]bool{}
		for _, d := range raw {
			s, ok := d.(string)
			if !ok || seen[s] || s == t.tid || t.p.tasks[s] == nil || t.p.dependsOnL(s, t.tid, nil) { // dropping cycles keeps waiting tasks startable
				continue
			}
			seen[s] = true
			deps = append(deps, s)
		}
		t.info.Dependencies = deps
	}
	t.p.touchL()
	t.p.m.saveL()
	t.publishL()
	if t.info.Waiting {
		t.p.startUnblockedL()
	}
	return nil
}

// blockedOnL lists the dependencies that still stand in the way: existing, unmerged tasks.
func (t *Task) blockedOnL() []string {
	var out []string
	for _, tid := range t.info.Dependencies {
		if dep := t.p.tasks[tid]; dep != nil && dep.info.Phase != PhaseDone {
			out = append(out, tid)
		}
	}
	return out
}

// overBudgetL: the budget (if any) leaves no meaningful room for another run.
func (t *Task) overBudgetL() bool {
	return t.info.Budget != nil && *t.info.Budget-t.info.Spent < 0.01
}

func (t *Task) noteBudgetL() {
	t.note(fmt.Sprintf("the task budget ($%g) is used up (spent $%.2f); raise it in the task settings to continue", *t.info.Budget, t.info.Spent))
}

// --- phase transitions ---

// Assign leaves the plan phase: create the workspace and hand the task over.
// With unresolved dependencies the task just parks in the agent column; once
// they are all merged or deleted, startUnblocked calls this again. The clone
// is made only then, so it includes the dependencies' merged work.
func (t *Task) Assign(to string) error {
	t.lock()
	if strings.TrimSpace(t.info.Title) == "" {
		t.unlock()
		return errors.New("Give the task a title first")
	}
	t.touchL()
	if to == "agent" && len(t.blockedOnL()) > 0 {
		t.info.Waiting = true
		t.setPhaseL(PhaseAgent)
		t.unlock()
		return nil
	}
	t.info.Waiting = false
	title, desc := t.info.Title, t.info.Description
	t.unlock()
	defer t.p.m.work()()
	if err := t.ensureClone(); err != nil {
		return err
	}
	if to == "agent" {
		prompt := initialPrompt(title, desc)
		e := newEntry("user")
		e.Text = prompt
		t.addEntry(e)
		t.kick(prompt, false)
	} else {
		t.lock()
		t.setPhaseL(PhaseHuman)
		t.unlock()
		t.bgUp()
	}
	return nil
}

// SendChat: a user chat message, shown in the log, then fed to (or starting) claude.
func (t *Task) SendChat(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	t.lock()
	if t.info.Phase == PhasePlan {
		t.unlock()
		return errors.New("Assign the task to the agent first")
	}
	if t.info.Waiting {
		t.unlock()
		return errors.New("This task is waiting for its dependencies; remove them in the settings to start it now")
	}
	t.touchL()
	t.p.touchL()
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
// on the merge.
func (t *Task) kick(text string, fresh bool) {
	t.lock()
	if t.overBudgetL() {
		t.noteBudgetL()
		if t.agentPhaseL() {
			t.setPhaseL(PhaseHuman)
		}
		t.unlock()
		return
	}
	if t.info.Phase != PhaseMerge {
		t.setPhaseL(PhaseAgent)
	}
	// A running claude has its spending cap fixed at start; a changed budget needs a new process.
	old := t.session
	restart := old != nil && (fresh || !sameBudget(t.sessionBudget, t.info.Budget))
	t.unlock()
	go func() {
		if restart {
			t.stopSession(old)
		}
		s, err := t.ensureSession(fresh)
		if err != nil {
			t.noteErr("agent start failed", err)
			t.lock()
			if t.agentPhaseL() {
				t.setPhaseL(PhaseHuman)
			}
			t.unlock()
			return
		}
		s.Send(text)
		t.lock()
		t.publishL()
		t.unlock()
	}()
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

// MoveTo maps board drags onto the real actions.
func (t *Task) MoveTo(phase Phase) error {
	t.lock()
	current, waiting := t.info.Phase, t.info.Waiting
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
		return t.SendChat("Please continue working on the task.")
	case PhaseHuman:
		if current == PhasePlan || waiting { // a waiting task has no clone yet
			return t.Assign("human")
		}
		return t.StopAgent()
	default:
		return t.Merge("")
	}
}

// Merge commits the working tree as one commit, rebases it onto the latest
// default branch if that moved, and fast-forwards the project repo. The task
// sits in the merge phase meanwhile: rebase conflicts are handed to a fresh
// agent there, whose 'ready' runs the merge again. A failure puts the task
// back with the human.
func (t *Task) Merge(message string) error {
	defer t.p.m.work()()
	repo := t.repoDir()
	if !exists(repo) {
		return errors.New("The task has no work to merge yet")
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
	if _, err := git(repo, "add", "-A"); err != nil {
		return err
	}
	if !gitOK(repo, "diff", "--cached", "--quiet") {
		if _, err := git(repo, "commit", "--no-verify", "-m", message); err != nil {
			return err
		}
	}
	if _, err := git(repo, "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	target, err := git(repo, "rev-parse", "origin/"+branch)
	if err != nil {
		return err
	}
	if !gitOK(repo, "merge-base", "--is-ancestor", target, "HEAD") {
		if _, err := git(repo, "rebase", target); err != nil {
			if !t.midRebase() {
				return fmt.Errorf("rebase onto %s failed", branch)
			}
			t.note(fmt.Sprintf("rebasing onto the latest %s hit conflicts; sending in a fresh agent to resolve them", branch))
			t.kick(conflictPrompt(branch, message), true)
			return nil
		}
	}
	if head, _ := git(repo, "rev-parse", "HEAD"); head != target {
		if err := t.p.fastForward(repo); err != nil {
			return err
		}
		t.note("merged into " + branch + " ✔")
	} else {
		t.note("nothing to merge; task closed")
	}
	t.lock()
	t.info.CommitMessage = ""
	t.setPhaseL(PhaseDone)
	t.p.startUnblockedL()
	t.unlock()
	go t.down()
	return nil
}

func (t *Task) midRebase() bool {
	return exists(filepath.Join(t.repoDir(), ".git", "rebase-merge")) || exists(filepath.Join(t.repoDir(), ".git", "rebase-apply"))
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
		t.container, t.lastTag, t.runCmd = c, tag, containerfileCmd(cf)
		t.status = StatusUp
	}
}

// Discard: back to plan, all work is thrown away (the UI asks for confirmation).
func (t *Task) Discard() error {
	defer t.p.m.work()()
	_ = t.StopAgent()
	t.down()
	rmContainer(t.containerName()) // also one the daemon never knew about
	if err := os.RemoveAll(t.dir()); err != nil {
		return err
	}
	t.lock()
	defer t.unlock()
	t.info.Started = false
	t.info.CommitMessage = ""
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
	if err := os.RemoveAll(t.dir()); err != nil {
		return err
	}
	t.lock()
	defer t.unlock()
	delete(t.p.info.Tasks, t.tid)
	delete(t.p.tasks, t.tid)
	t.p.m.hub.Set([]string{"projects", t.p.pid, "tasks", t.tid}, nil)
	t.p.m.hub.SetChat(t.key(), nil)
	t.p.touchL()
	t.p.m.saveL()
	t.p.startUnblockedL()
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
}

// --- workspace (clone + container) ---

func (t *Task) ensureClone() error {
	if exists(t.repoDir()) {
		return nil
	}
	if err := os.MkdirAll(t.claudeDir(), 0o755); err != nil {
		return err
	}
	// A local git clone hardlinks the object store (objects are immutable,
	// so sharing them is safe): nearly free even for big repos.
	if _, err := runCmd([]string{"git", "clone", "--quiet", "-b", t.p.defaultBranch, t.p.dir(), t.repoDir()}, RunOpts{}); err != nil {
		return err
	}
	if _, err := git(t.repoDir(), "config", "user.name", "TPS"); err != nil {
		return err
	}
	_, err := git(t.repoDir(), "config", "user.email", "tps@localhost")
	return err
}

// Open is called when a user opens the task's page: bring the workspace up.
func (t *Task) Open() {
	t.lock()
	t.touchL()
	phase := t.info.Phase
	t.unlock()
	if phase != PhasePlan && exists(t.repoDir()) {
		t.bgUp()
	}
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
		return nil, errors.New("The task has no workspace (still in plan?)")
	}
	cf := t.containerfile()
	tag := imageTag(cf)
	t.lock()
	// Don't recycle a container out from under a live claude session or run.
	if t.container != nil && t.status == StatusUp && (t.lastTag == tag || t.session != nil || t.run != nil) {
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
	if !imageExists(tag) {
		t.note("building the dev container image; the first build takes a few minutes…")
	}
	if err := buildImage(tag, cf, t.repoDir(), func(string) {}); err != nil {
		return t.failUp(err)
	}
	t.lock()
	t.setStatusL(StatusStarting, "starting container")
	t.unlock()
	c, err := ensureContainer(containerOpts{name: t.containerName(), image: tag, toolbox: toolbox, repoDir: t.repoDir(), claudeDir: t.claudeDir()})
	if err != nil {
		return t.failUp(err)
	}
	t.lock()
	t.container, t.lastTag, t.runCmd = c, tag, containerfileCmd(cf)
	t.setStatusL(StatusUp, "")
	t.unlock()
	return c, nil
}

func (t *Task) down() {
	t.lock()
	if t.status == StatusDown && t.container == nil {
		t.unlock()
		return
	}
	t.setStatusL(StatusStopping, "")
	s, r, c := t.session, t.run, t.container
	t.unlock()
	if s != nil {
		s.Kill()
	}
	if r != nil {
		r.Kill()
	}
	if c != nil {
		c.Rm()
	}
	t.lock()
	t.container = nil
	t.setStatusL(StatusDown, "")
	t.unlock()
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
		OnTurnEnd: func(delta float64) { go t.onTurnEnd(delta) },
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

// onTurnEnd: a claude turn finished. Account the cost, consume the done file, move the task along.
func (t *Task) onTurnEnd(costDelta float64) {
	t.lock()
	t.touchL()
	t.p.touchL()
	if costDelta > 0 {
		t.info.Spent = math.Round((t.info.Spent+costDelta)*10000) / 10000
		t.p.m.saveL()
	}
	t.publishL()
	if t.workingL() { // the user already sent a follow-up
		t.unlock()
		return
	}
	t.unlock()
	go t.refreshChanges()
	doneFile := filepath.Join(t.repoDir(), DoneFile)
	content, readErr := os.ReadFile(doneFile)
	_ = os.Remove(doneFile)
	t.lock()
	if !t.agentPhaseL() { // stopped or dragged elsewhere meanwhile
		t.unlock()
		return
	}
	word, rest := "", ""
	if readErr == nil {
		word, rest, _ = strings.Cut(string(content), "\n")
		word = strings.TrimSpace(word)
	}
	if word == "reload" && !t.overBudgetL() {
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
	if t.info.Phase == PhaseMerge { // the agent was resolving rebase conflicts
		msg := t.info.CommitMessage
		if word != "ready" {
			if t.overBudgetL() {
				t.noteBudgetL()
			}
			t.note("merge paused: send the agent back in to finish the rebase, or finish it in VS Code, then merge again")
			t.setPhaseL(PhaseHuman)
			t.unlock()
			return
		}
		t.unlock()
		t.note("conflicts resolved; merging")
		_ = t.Merge(msg)
		return
	}
	if word == "ready" {
		t.info.CommitMessage = strings.TrimSpace(rest)
		if t.info.CommitMessage == "" {
			t.info.CommitMessage = t.info.Title
		}
		t.p.m.saveL()
		auto := t.p.info.AutoMerge
		if t.info.AutoMerge != nil {
			auto = *t.info.AutoMerge
		}
		if auto {
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
	if t.agentPhaseL() && !t.stopping {
		if code != 0 {
			t.note(fmt.Sprintf("claude exited unexpectedly (%d)", code), errTail)
		}
		t.setPhaseL(PhaseHuman)
	}
}

// --- running the project: the Containerfile's CMD, in the task's container ---

func (t *Task) Run() error {
	c, err := t.up()
	if err != nil {
		return err
	}
	t.lock()
	if t.runCmd == nil {
		t.unlock()
		return errors.New("Containerfile.dev has no CMD line saying how to run this project")
	}
	if t.run != nil {
		t.unlock()
		return errors.New("Already running")
	}
	s := newRunSession(c, t.runCmd, func(tail string) {
		t.lock()
		t.pubL("runLog", tail)
		t.unlock()
	}, t.onRunExit)
	// Assigned before the spawn, so a second Run meanwhile sees it.
	t.run, t.runCode = s, nil
	t.touchL()
	t.pubL("runLog", "")
	t.publishL()
	t.unlock()
	if err := s.start(); err != nil {
		t.lock()
		t.run = nil
		t.publishL()
		t.unlock()
		return err
	}
	return nil
}

func (t *Task) StopRun() error {
	t.lock()
	r := t.run
	t.unlock()
	if r != nil {
		r.Stop()
	}
	return nil
}

func (t *Task) onRunExit(s *runSession, code int) {
	t.lock()
	if t.run == s {
		t.run = nil
		t.runCode = &code
	}
	t.publishL()
	t.unlock()
	if code != 0 && !s.stopped.Load() {
		t.note(fmt.Sprintf("run exited (%d)", code))
	}
}

// --- the 'live' indicator: is something answering on the task's $PORT? ---

func (t *Task) setLiveL(live bool) {
	if live == t.live {
		return
	}
	t.live = live
	t.pubL("live", live)
}
