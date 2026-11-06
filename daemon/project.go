package daemon

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"slices"
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
	Pid      string               `json:"pid,omitempty"` // what its tasks directory is named after (see load)
	Dir      string               `json:"dir"`
	Name     string               `json:"name"`
	Color    string               `json:"color,omitempty"`   // the accent the dashboards show it in, as #rrggbb
	Scratch  bool                 `json:"scratch,omitempty"` // the host's home of tasks that belong to no project (see ensureScratch)
	Defaults TaskDefaults         `json:"defaults"`
	Activity int64                `json:"activity,omitempty"` // unix ms of the last change to a task
	NextTask int                  `json:"nextTask,omitempty"`
	Tasks    map[string]*TaskInfo `json:"tasks"`

	// The tasks being merged, the first merging and the rest waiting their
	// turn (see queue.go).
	Queue []string `json:"queue,omitempty"`

	// Whether a merge deploys by itself (see deploy.go), the host's files the
	// containers of its tasks get (see Task.mountsL), and what of its checkout
	// their workspaces start with (see Task.seed).
	AutoDeploy bool     `json:"autoDeploy,omitempty"`
	Mounts     []Mount  `json:"mounts,omitempty"`
	Seeds      []string `json:"seeds,omitempty"`
}

// Mount is a file or directory of the host a project's containers get:
// read-only unless Write, and the deploy's alone unless AllTasks.
type Mount struct {
	Path     string `json:"path"`
	Write    bool   `json:"write,omitempty"`
	AllTasks bool   `json:"allTasks,omitempty"`
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

	deploy       string // what Containerfile.dev on the default branch declares as the deploy (see deploy.go)
	deploying    bool   // a deploy runs
	deployWanted bool   // and another is wanted after it,
	deployNext   string // of this commit (the tip, for none)
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
	if info.Name == "" { // none was given
		info.Name = filepath.Base(info.Dir)
	}
	info.Defaults.Model = cmp.Or(bareModel(info.Defaults.Model), DefaultModel)
	info.Defaults.ReviewModel = bareModel(info.Defaults.ReviewModel)
	for _, t := range info.Tasks {
		t.Model, t.ReviewModel = bareModel(t.Model), bareModel(t.ReviewModel)
	}
	if !colorRe.MatchString(info.Color) {
		info.Color = m.pickColorL()
	}
	info.NextTask = max(1, info.NextTask)
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
	p.pubScratch()
	for _, tid := range slices.Sorted(maps.Keys(p.info.Tasks)) {
		info := p.info.Tasks[tid]
		if tid == "train" { // the job of an older TPS, which merged through a train
			delete(p.info.Tasks, tid)
			_ = rmContainer("tps-" + p.pid + "-" + tid)
			_ = rmTree(filepath.Join(p.tasksDir(), tid))
			continue
		}
		t := newTask(p, tid, info)
		p.m.mu.Lock()
		p.tasks[tid] = t
		p.m.mu.Unlock()
		t.loadChat()
		if t.hasWorkspace() {
			if err := matchRemotes(t.repoDir(), p.dir()); err != nil {
				t.noteErr("Giving the workspace the project's remotes failed", err)
			}
		}
		p.m.mu.Lock()
		agentPhase := info.Phase == PhaseMerge || info.Phase == PhaseAgent
		switch {
		case t.job() && info.Phase == PhaseMerge:
			t.note("TPS restarted during the deploy; deploy again to be sure it went through")
			info.Phase = PhaseHuman
		case info.Phase == PhaseMerge && info.LimitUntil == 0:
			// Queued: a merge under way starts over (see below).
			if !slices.Contains(p.info.Queue, tid) {
				p.info.Queue = append(p.info.Queue, tid)
			}
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
	p.pubSetup()
	p.m.mu.Lock()
	p.info.Queue = slices.DeleteFunc(p.info.Queue, func(tid string) bool {
		t := p.tasks[tid]
		return t == nil || t.info.Phase != PhaseMerge
	})
	p.pubQueueL()
	p.startHeadL()
	p.m.mu.Unlock()
	return nil
}

// pubSetup publishes the settings on what reaches its containers.
func (p *Project) pubSetup() {
	p.pub("autoDeploy", p.info.AutoDeploy)
	p.pub("mounts", p.info.Mounts)
	p.pub("seeds", p.info.Seeds)
}

// pubScratch tells the dashboards a scratch project apart.
func (p *Project) pubScratch() {
	if p.info.Scratch {
		p.pub("scratch", true)
	}
}

// touchL records activity, for dashboards that order projects by it.
func (p *Project) touchL() {
	p.info.Activity = time.Now().UnixMilli()
	p.pub("activity", p.info.Activity)
}

// waitsForL: true when task tid (transitively) waits for task on.
func (p *Project) waitsForL(tid, on string, seen map[string]bool) bool {
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
		for _, d := range t.info.WaitFor {
			if p.waitsForL(d, on, seen) {
				return true
			}
		}
	}
	return false
}

// endWaitsL ends the wait of every task whose wait is over (see waitOverL). It
// is called wherever that may have just changed: a task reaching Done or being
// deleted, and a setting changed.
func (p *Project) endWaitsL() {
	for _, t := range p.tasks {
		if t.waitOverL() {
			t.ending = true
			go t.endWait()
		}
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
		t.dropContainer()
	}
}

func (p *Project) taskListL() []*Task {
	return slices.Collect(maps.Values(p.tasks))
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
	cf, _ := git(p.dir(), "show", p.defaultBranch+":"+containerfile)
	declared := containerfileDeclarations(cf)
	deploy := declared.deploy
	p.pub("test", declared.test != "") // whether a merge may skip one
	p.m.mu.Lock()
	moved := head != "" && head != p.head
	p.head, p.deploy = head, deploy
	tasks := p.taskListL()
	p.m.mu.Unlock()
	p.refreshBranch()
	p.retryHeld()
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
	partial, err := checkSettings(partial)
	if err != nil {
		return err
	}
	if name, ok := partial["name"].(string); ok {
		p.info.Name = name
	}
	if color, ok := partial["color"].(string); ok {
		p.info.Color = color
	}
	if auto, ok := partial["autoDeploy"].(bool); ok {
		p.info.AutoDeploy = auto
	}
	if mounts, ok := partial["mounts"].([]Mount); ok {
		p.info.Mounts = mounts
		// The deploy's container gets them when it is made anew; a task's when
		// its own is, as it is after idling.
		if t := p.tasks[deployTid]; t != nil && !p.deploying {
			go t.down()
		}
	}
	if seeds, ok := partial["seeds"].([]string); ok {
		p.info.Seeds = seeds // for the workspaces made from now on (see Task.seed)
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
	p.pubSetup()
	return nil
}

// pubDefaults publishes the defaults a field at a time, so that setting one
// leaves the rest of them — the field being typed in included — alone.
func (p *Project) pubDefaults() {
	for field, value := range p.defaultsL() {
		p.m.hub.Set([]string{"projects", p.pid, "defaults", field}, value)
	}
}

// defaultsL are the defaults as they are shown: what a task gets for each.
func (p *Project) defaultsL() map[string]any {
	d := p.info.Defaults
	return map[string]any{
		"model":            d.Model,
		"reviewModel":      d.ReviewModel,
		"onReady":          string(cmp.Or(d.OnReady, defaultOnReady)),
		"onAccept":         string(cmp.Or(d.OnAccept, defaultOnAccept)),
		"reviewLoops":      reviewLoops(d.ReviewLoops),
		"budget":           optional(d.Budget),
		"openrouterBudget": optional(d.ORBudget),
	}
}

// CreateTask writes a task down, description and settings and all. The
// dashboard creates one the moment it is started, empty and in Plan, so it is
// on the board and in the sidebar while its description is being written. A
// partial naming a schedule makes a scheduled task (see schedule.go), whose
// runs nobody watches: they have their work reviewed again and again, and
// merged once a review accepts it.
func (p *Project) CreateTask(partial map[string]any) (string, error) {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	// The project's defaults are what a task starts out with; whatever the
	// partial names wins.
	d := p.info.Defaults // a copy, so the task's settings are its own
	info := &TaskInfo{Model: d.Model, ReviewModel: d.ReviewModel, OnReady: d.OnReady, OnAccept: d.OnAccept,
		ReviewLoops: copyPtr(d.ReviewLoops), Budget: copyPtr(d.Budget), ORBudget: copyPtr(d.ORBudget), Phase: PhasePlan, PhaseAt: time.Now().UnixMilli()}
	if _, ok := partial["schedule"]; ok {
		loops := maxReviewLoops
		info.Schedule, info.OnReady, info.OnAccept, info.ReviewLoops = newSchedule(), AnswerReview, AnswerMerge, &loops
	}
	// A task an agent asked for (see board.go) says by whom until the user has looked at it.
	info.Requested, _ = partial["requested"].(string)
	t := p.addTaskL(info)
	t.applyL(partial)
	p.m.saveL()
	t.publishL()
	return t.tid, nil
}

// nextTidL takes the project's next task number.
func (p *Project) nextTidL() string {
	tid := strconv.Itoa(p.info.NextTask)
	p.info.NextTask++
	return tid
}

// addTaskL writes a new task down under the project's next tid, and shows it.
func (p *Project) addTaskL(info *TaskInfo) *Task {
	tid := p.nextTidL()
	p.info.Tasks[tid] = info
	t := newTask(p, tid, info)
	p.tasks[tid] = t
	p.touchL()
	p.m.saveL()
	t.publishL()
	return t
}

// fastForward moves the project repo's default branch to a ref of a task's clone.
func (p *Project) fastForward(fromDir, ref string) error {
	if _, err := git(p.dir(), "fetch", "--quiet", fromDir, ref); err != nil {
		return err
	}
	sha, err := git(p.dir(), "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if branch, _ := git(p.dir(), "symbolic-ref", "--short", "HEAD"); branch == p.defaultBranch {
		// The user's checkout: a real merge keeps their working tree in sync.
		_, err = git(p.dir(), "merge", "--ff-only", sha)
	} else if !gitOK(p.dir(), "merge-base", "--is-ancestor", p.defaultBranch, sha) {
		err = fmt.Errorf("%s moved meanwhile: not a fast-forward", p.defaultBranch)
	} else {
		_, err = git(p.dir(), "update-ref", "refs/heads/"+p.defaultBranch, sha)
	}
	if err != nil {
		return err
	}
	p.refreshMeta()
	return nil
}
