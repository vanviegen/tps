package daemon

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TaskDefaults is what a project hands to the tasks created in it. These are
// copied at creation, not consulted afterwards: changing them says what the
// next tasks start out with, and leaves the ones that exist alone.
type TaskDefaults struct {
	Model       string   `json:"model,omitempty"`
	ReviewModel string   `json:"reviewModel,omitempty"`
	OnReady     Answer   `json:"onReady,omitempty"`
	OnAccept    Answer   `json:"onAccept,omitempty"`
	ReviewLoops *int     `json:"reviewLoops,omitempty"`
	Budget      *float64 `json:"budget,omitempty"`
	ORBudget    *float64 `json:"openrouterBudget,omitempty"`
}

// ProjectInfo is the persisted part of a project (in projects.json). The list
// belongs to the host: every dashboard connecting to this daemon sees the same
// projects, under the same names.
type ProjectInfo struct {
	Dir      string               `json:"dir"`
	Name     string               `json:"name"`
	Color    string               `json:"color,omitempty"` // the accent the dashboards show it in, as #rrggbb
	Defaults TaskDefaults         `json:"defaults"`
	Activity int64                `json:"activity,omitempty"` // unix ms of the last change to a task
	NextTask int                  `json:"nextTask,omitempty"`
	Tasks    map[string]*TaskInfo `json:"tasks"`
}

// Project: a registered git repo plus its tasks.
type Project struct {
	m             *Manager
	pid           string
	info          *ProjectInfo
	tasks         map[string]*Task
	defaultBranch string
	code          *codeServer // VS Code on the checkout itself; see code.go
	codeShown     bool        // a dashboard shows the checkout in VS Code
	codeSeen      time.Time   // when one last started or stopped doing so
	head          string      // the default branch's tip at the last refreshMeta; a new one dates every workspace
}

// projectColors are the accents a project may wear: hues that sit well on the
// dashboard's dark surfaces and apart from one another, so a colour tells
// projects apart where a name would not fit. A new project gets one of the
// least used, at random.
var projectColors = []string{
	"#5b9cf5", "#9b7bf0", "#e07bd6", "#f06b8a", "#d9a441",
	"#c8d35a", "#7bd36f", "#45c4d6", "#f2d35b", "#b98a6a",
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// pickColorL chooses a colour for a new project: one of those the fewest
// projects on this host wear already.
func (m *Manager) pickColorL() string {
	used := map[string]int{}
	for _, p := range m.projects {
		used[p.info.Color]++
	}
	least := math.MaxInt
	var candidates []string
	for _, c := range projectColors {
		if used[c] < least {
			least, candidates = used[c], nil
		}
		if used[c] == least {
			candidates = append(candidates, c)
		}
	}
	return candidates[rand.IntN(len(candidates))]
}

func newProject(m *Manager, pid string, info *ProjectInfo) *Project {
	if info.Tasks == nil {
		info.Tasks = map[string]*TaskInfo{}
	}
	if info.Name == "" { // added before the daemon kept names, or none was given
		info.Name = filepath.Base(info.Dir)
	}
	if info.Defaults.Model == "" {
		info.Defaults.Model = DefaultModel
	}
	if !colorRe.MatchString(info.Color) {
		info.Color = m.pickColorL()
	}
	if info.NextTask == 0 {
		for tid := range info.Tasks {
			if n, err := strconv.Atoi(tid); err == nil && n >= info.NextTask {
				info.NextTask = n + 1
			}
		}
		info.NextTask = max(1, info.NextTask)
	}
	return &Project{m: m, pid: pid, info: info, tasks: map[string]*Task{}, defaultBranch: "main"}
}

func (p *Project) dir() string      { return p.info.Dir }
func (p *Project) tasksDir() string { return filepath.Join(p.m.dataDir, "tasks", p.pid) }
func (p *Project) cacheDir() string { return filepath.Join(p.m.dataDir, "cache", p.pid) } // the caches its tasks share (see cacheLabel)
func (p *Project) pub(field string, value any) {
	p.m.hub.Set([]string{"projects", p.pid, field}, value)
}

func (p *Project) init() error {
	branch, _ := git(p.dir(), "symbolic-ref", "--short", "HEAD")
	if branch == "" {
		branch = "master"
		if gitOK(p.dir(), "rev-parse", "--verify", "-q", "main") {
			branch = "main"
		}
	}
	p.defaultBranch = branch
	p.m.hub.Set([]string{"projects", p.pid}, map[string]any{"dir": p.dir(), "name": p.info.Name, "defaults": map[string]any{}, "activity": p.info.Activity, "tasks": map[string]any{}})
	p.pubDefaults()
	p.pub("color", p.info.Color)
	tids := make([]string, 0, len(p.info.Tasks))
	for tid := range p.info.Tasks {
		tids = append(tids, tid)
	}
	sort.Strings(tids)
	for _, tid := range tids {
		info := p.info.Tasks[tid]
		t := newTask(p, tid, info)
		p.m.mu.Lock()
		p.tasks[tid] = t
		p.m.mu.Unlock()
		t.loadChat()
		p.m.mu.Lock()
		agentPhase := info.Phase == PhaseMerge || info.Phase == PhaseAgent
		switch {
		case info.Phase == PhaseReview:
			// The reviewer died with the daemon, and it was nobody's
			// conversation: there is nothing to pick back up, and the agent's
			// own turn had ended before it started.
			t.note("TPS restarted during the automated review; the task is yours")
			info.Phase = PhaseHuman
		case agentPhase && info.LimitUntil > 0:
			// Waiting out a usage limit rather than working: no turn was cut
			// off, and the wait is the daemon's to take back up.
			t.note("TPS restarted; the task is still waiting for claude's usage limit to reset")
			t.armLimitL(time.UnixMilli(info.LimitUntil))
		case agentPhase: // the daemon restarted mid-turn
			t.note("TPS restarted while the agent was working; send a message to continue")
			info.Phase = PhaseHuman
			t.queueL("restart", restartedPrompt) // saves the phase above with it
		default:
			info.LimitUntil = 0 // a wait the phase it was left in outlived
		}
		t.adoptL()
		t.publishL()
		p.m.mu.Unlock()
	}
	p.refreshMeta()
	return nil
}

// touchL records activity, for dashboards that order projects by it.
func (p *Project) touchL() {
	p.info.Activity = time.Now().UnixMilli()
	p.pub("activity", p.info.Activity)
}

// startsAfterL: true when task tid (transitively) follows task on.
func (p *Project) startsAfterL(tid, on string, seen map[string]bool) bool {
	if tid == on {
		return true
	}
	if seen == nil {
		seen = map[string]bool{}
	}
	if seen[tid] {
		return false
	}
	seen[tid] = true
	if t := p.tasks[tid]; t != nil {
		for _, d := range t.info.StartAfter {
			if p.startsAfterL(d, on, seen) {
				return true
			}
		}
	}
	return false
}

// autoStartL hands over every task whose wait is over (see autoStartableL). It
// is called wherever that may have just changed: a task reaching Done or being
// deleted, a setting changed, and a plan being closed.
func (p *Project) autoStartL() {
	for _, t := range p.tasks {
		if !t.autoStartableL() {
			continue
		}
		t.autoStarting = true
		go func() {
			err := t.Assign("agent")
			t.lock()
			t.autoStarting = false
			t.unlock()
			if err != nil {
				logf("auto-start %s: %v", t.key(), err)
			}
		}()
	}
}

func (p *Project) close() {
	p.closeCode()
	p.m.mu.Lock()
	tasks := p.taskListL()
	p.m.mu.Unlock()
	for _, t := range tasks {
		t.down()
	}
}

// forget is close plus removing the containers, for a project being unregistered.
func (p *Project) forget() {
	p.close()
	p.m.mu.Lock()
	tasks := p.taskListL()
	p.m.mu.Unlock()
	for _, t := range tasks {
		nestDown(t.containerName(), nestDir(t.dir()))
		_ = rmContainer(t.containerName())
	}
}

func (p *Project) taskListL() []*Task {
	out := make([]*Task, 0, len(p.tasks))
	for _, t := range p.tasks {
		out = append(out, t)
	}
	return out
}

func (p *Project) refreshMeta() {
	p.pub("defaultBranch", p.defaultBranch)
	branch, _ := git(p.dir(), "branch", "--show-current") // empty on a detached HEAD
	p.pub("branch", branch)
	status, _ := git(p.dir(), "status", "--porcelain")
	p.pub("dirty", status != "")
	p.pub("git", gitSummary(status))
	// A branch that moved — a merge, or a commit made in the checkout — leaves
	// every workspace a little further behind; they count again (see refreshBehind).
	head, _ := git(p.dir(), "rev-parse", p.defaultBranch)
	p.m.mu.Lock()
	moved := head != "" && head != p.head
	p.head = head
	tasks := p.taskListL()
	p.m.mu.Unlock()
	if !moved {
		return
	}
	for _, t := range tasks {
		go t.refreshBehind()
	}
}

// gitSummary boils `git status --porcelain` down to the few words the
// dashboard has room for: "Clean", or what is changed and what is new.
func gitSummary(status string) string {
	changed, added := 0, 0
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "??") {
			added++
		} else {
			changed++
		}
	}
	var parts []string
	if changed > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", changed))
	}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d new", added))
	}
	if len(parts) == 0 {
		return "Clean"
	}
	return strings.Join(parts, ", ")
}

func (p *Project) SetConfig(partial map[string]any) error {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	if name, ok := partial["name"].(string); ok {
		if name = strings.TrimSpace(name); name == "" {
			return errors.New("Give the project a name")
		}
		p.info.Name = name
	}
	if color, ok := partial["color"].(string); ok {
		if !colorRe.MatchString(color) {
			return errors.New("A colour is #rrggbb")
		}
		p.info.Color = strings.ToLower(color)
	}
	// The defaults arrive as the task settings they are, one or more at a time.
	if defaults, ok := partial["defaults"].(map[string]any); ok {
		d := &p.info.Defaults
		if model, ok := defaults["model"].(string); ok && model != "" {
			d.Model = model
		}
		// Empty is a setting of its own here: the reviewer takes the task's model.
		if model, ok := defaults["reviewModel"].(string); ok {
			d.ReviewModel = model
		}
		if answer, ok := parseAnswer(defaults["onReady"], AnswerHuman, AnswerReview, AnswerMerge); ok {
			d.OnReady = answer
		}
		if answer, ok := parseAnswer(defaults["onAccept"], AnswerHuman, AnswerMerge); ok {
			d.OnAccept = answer
		}
		if loops, ok := parseLoops(defaults["reviewLoops"]); ok {
			d.ReviewLoops = &loops
		}
		if budget, ok := defaults["budget"]; ok {
			d.Budget = parseBudget(budget)
		}
		if budget, ok := defaults["openrouterBudget"]; ok {
			d.ORBudget = parseBudget(budget)
		}
	}
	p.m.saveL()
	p.pub("name", p.info.Name)
	p.pubDefaults()
	p.pub("color", p.info.Color)
	return nil
}

// pubDefaults publishes the defaults a field at a time, so that setting one
// leaves the rest of them — the field being typed in included — alone.
func (p *Project) pubDefaults() {
	d := p.info.Defaults
	p.pubDefault("model", d.Model)
	p.pubDefault("reviewModel", d.ReviewModel)
	p.pubDefault("onReady", string(cmp.Or(d.OnReady, defaultOnReady)))
	p.pubDefault("onAccept", string(cmp.Or(d.OnAccept, defaultOnAccept)))
	p.pubDefault("reviewLoops", reviewLoops(d.ReviewLoops))
	p.pubDefault("budget", optional(d.Budget))
	p.pubDefault("openrouterBudget", optional(d.ORBudget))
}

func (p *Project) pubDefault(field string, value any) {
	p.m.hub.Set([]string{"projects", p.pid, "defaults", field}, value)
}

// CreateTask writes a task down, description and settings and all. The
// dashboard creates one the moment it is started, empty and in Plan, so it is
// on the board and in the sidebar while its description is being written.
func (p *Project) CreateTask(partial map[string]any) (string, error) {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	tid := strconv.Itoa(p.info.NextTask)
	p.info.NextTask++
	// The project's defaults are what a task starts out with; whatever the
	// partial names wins.
	d := p.info.Defaults // a copy, so the task's settings are its own
	info := &TaskInfo{Model: d.Model, ReviewModel: d.ReviewModel, OnReady: d.OnReady, OnAccept: d.OnAccept,
		ReviewLoops: copyPtr(d.ReviewLoops), Budget: copyPtr(d.Budget), ORBudget: copyPtr(d.ORBudget), Phase: PhasePlan, PhaseAt: time.Now().UnixMilli()}
	p.info.Tasks[tid] = info
	t := newTask(p, tid, info)
	p.tasks[tid] = t
	t.applyL(partial)
	p.touchL()
	p.m.saveL()
	t.publishL()
	return tid, nil
}

// fastForward moves the project repo's default branch to the task clone's HEAD.
func (p *Project) fastForward(fromDir string) error {
	if _, err := git(p.dir(), "fetch", "--quiet", fromDir, p.defaultBranch); err != nil {
		return err
	}
	sha, err := git(p.dir(), "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if branch, _ := git(p.dir(), "symbolic-ref", "--short", "HEAD"); branch == p.defaultBranch {
		// The user's checkout: a real merge keeps their working tree in sync.
		_, err = git(p.dir(), "merge", "--ff-only", sha)
	} else {
		_, err = git(p.dir(), "update-ref", "refs/heads/"+p.defaultBranch, sha)
	}
	if err != nil {
		return err
	}
	p.refreshMeta()
	return nil
}
