package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// ProjectInfo is the persisted part of a project (in projects.json).
type ProjectInfo struct {
	Dir       string               `json:"dir"`
	Name      string               `json:"name"`
	AutoMerge bool                 `json:"autoMerge,omitempty"` // merge without confirmation when the agent reports ready
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
	p.m.hub.Set([]string{"projects", p.pid}, map[string]any{"name": p.info.Name, "dir": p.dir(), "autoMerge": p.info.AutoMerge, "tasks": map[string]any{}})
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
		if info.Phase == PhaseAgent && !info.Waiting { // the daemon restarted mid-turn
			t.note("TPS restarted while the agent was working; send a message to continue")
			info.Phase = PhaseHuman
		}
		t.adoptL()
		t.publishL()
		p.m.mu.Unlock()
	}
	p.refreshMeta()
	p.m.mu.Lock()
	p.startUnblockedL()
	p.m.mu.Unlock()
	return nil
}

// dependsOnL: true when task tid (transitively) depends on task on.
func (p *Project) dependsOnL(tid, on string, seen map[string]bool) bool {
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
		for _, d := range t.info.Dependencies {
			if p.dependsOnL(d, on, seen) {
				return true
			}
		}
	}
	return false
}

// startUnblockedL starts any task that was waiting on dependencies now merged or gone.
func (p *Project) startUnblockedL() {
	for _, t := range p.tasks {
		if t.info.Waiting && len(t.blockedOnL()) == 0 {
			go func() {
				if err := t.Assign("agent"); err != nil {
					logf("autostart %s: %v", t.key(), err)
				}
			}()
		}
	}
}

func (p *Project) close() {
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
	needsSetup := !exists(filepath.Join(p.dir(), containerfile))
	p.pub("defaultBranch", p.defaultBranch)
	p.pub("needsSetup", needsSetup)
	status, _ := git(p.dir(), "status", "--porcelain")
	p.pub("dirty", status != "")
	if needsSetup {
		var names []string
		if entries, err := os.ReadDir(p.dir()); err == nil {
			for _, e := range entries {
				names = append(names, e.Name())
			}
		}
		p.pub("detected", detectTools(names))
	}
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

// CreateContainerfile generates Containerfile.dev in the repo root and commits it.
func (p *Project) CreateContainerfile(toolIDs []string) error {
	defer p.m.work()()
	if branch, _ := git(p.dir(), "symbolic-ref", "--short", "HEAD"); branch != p.defaultBranch {
		return fmt.Errorf("Check out the %s branch first (or commit a %s yourself)", p.defaultBranch, containerfile)
	}
	if err := os.WriteFile(filepath.Join(p.dir(), containerfile), []byte(generateContainerfile(toolIDs)), 0o644); err != nil {
		return err
	}
	if _, err := git(p.dir(), "add", containerfile); err != nil {
		return err
	}
	if _, err := git(p.dir(), "commit", "--no-verify", "-m", "Add "+containerfile+" (TPS dev container)", "--", containerfile); err != nil {
		return err
	}
	p.refreshMeta()
	return nil
}

// CreateTask: tasks start empty; the plan page is where they get their title.
func (p *Project) CreateTask() (string, error) {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	tid := strconv.Itoa(p.info.NextTask)
	p.info.NextTask++
	info := &TaskInfo{Model: "sonnet", Phase: PhasePlan}
	p.info.Tasks[tid] = info
	t := newTask(p, tid, info)
	p.tasks[tid] = t
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
