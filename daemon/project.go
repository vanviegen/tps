package daemon

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProjectInfo is the persisted part of a project (in projects.json). Projects
// are known by their directory; what to call them is up to each dashboard.
type ProjectInfo struct {
	Dir       string               `json:"dir"`
	AutoMerge bool                 `json:"autoMerge,omitempty"` // merge without confirmation when the agent reports ready
	Activity  int64                `json:"activity,omitempty"`  // unix ms of the last change to a task
	NextTask  int                  `json:"nextTask,omitempty"`
	Tasks     map[string]*TaskInfo `json:"tasks"`
}

// Project: a registered git repo plus its tasks.
type Project struct {
	m             *Manager
	pid           string
	info          *ProjectInfo
	tasks         map[string]*Task
	defaultBranch string
	code          *codeServer // VS Code on the checkout itself; see code.go
}

func newProject(m *Manager, pid string, info *ProjectInfo) *Project {
	if info.Tasks == nil {
		info.Tasks = map[string]*TaskInfo{}
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
	p.m.hub.Set([]string{"projects", p.pid}, map[string]any{"dir": p.dir(), "autoMerge": p.info.AutoMerge, "activity": p.info.Activity, "tasks": map[string]any{}})
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
		if info.Phase == PhaseMerge || info.Phase == PhaseAgent { // the daemon restarted mid-turn
			t.note("TPS restarted while the agent was working; send a message to continue")
			info.Phase = PhaseHuman
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
		rmContainer(t.containerName())
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
	if auto, ok := partial["autoMerge"]; ok {
		p.info.AutoMerge = auto == true
	}
	p.m.saveL()
	p.pub("autoMerge", p.info.AutoMerge)
	return nil
}

// CreateTask writes a task down, description and settings and all. A task is
// only created once there is something worth keeping: the dashboard drafts one
// in the browser and sends it here when it is assigned, so a task the user
// thought better of never reaches this file.
func (p *Project) CreateTask(partial map[string]any) (string, error) {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	tid := strconv.Itoa(p.info.NextTask)
	p.info.NextTask++
	info := &TaskInfo{Model: DefaultModel, Phase: PhasePlan}
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
