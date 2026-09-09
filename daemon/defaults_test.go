package daemon

import (
	"testing"

	"github.com/vanviegen/agent-manager/hub"
)

func testManager() *Manager {
	return &Manager{projects: map[string]*Project{}, hub: hub.New(nil), saveCh: make(chan []byte, 1)}
}

// A task is created on the project's defaults, with whatever the dashboard
// sends along on top of them.
func TestCreateTaskDefaults(t *testing.T) {
	m := testManager()
	budget := 5.0
	p := newProject(m, "project", &ProjectInfo{Dir: "/tmp/project", Name: "project",
		Defaults: TaskDefaults{Model: "opus", Budget: &budget, AutoMerge: true}})
	m.projects[p.pid] = p

	tid, err := p.CreateTask(map[string]any{"description": "do it"})
	if err != nil {
		t.Fatal(err)
	}
	info := p.info.Tasks[tid]
	if info.Model != "opus" || info.Budget == nil || *info.Budget != 5 || info.AutoMerge == nil || !*info.AutoMerge {
		t.Errorf("defaults not copied: %+v", info)
	}
	// The copy is the task's own: raising its budget is not raising the default.
	*info.Budget = 99
	if *p.info.Defaults.Budget != 5 {
		t.Error("the default budget was changed along with the task's")
	}

	tid, err = p.CreateTask(map[string]any{"description": "do it my way", "model": "haiku", "budget": "1.5", "autoMerge": false})
	if err != nil {
		t.Fatal(err)
	}
	info = p.info.Tasks[tid]
	if info.Model != "haiku" || info.Budget == nil || *info.Budget != 1.5 || info.AutoMerge == nil || *info.AutoMerge {
		t.Errorf("the task's own settings should win: %+v", info)
	}
}

// Projects used to hold the merge setting themselves, with a task's own
// overriding it; both readings must survive the move to the defaults.
func TestAutoMergeMigration(t *testing.T) {
	auto, never := true, false
	p := newProject(testManager(), "project", &ProjectInfo{Dir: "/tmp/project", AutoMerge: &auto, Tasks: map[string]*TaskInfo{
		"1": {Phase: PhaseHuman},
		"2": {Phase: PhaseHuman, AutoMerge: &never},
	}})
	if !p.info.Defaults.AutoMerge {
		t.Error("the project setting should become the default")
	}
	if p.info.AutoMerge != nil {
		t.Error("the old field should be cleared, so it is not read again")
	}
	if info := p.info.Tasks["1"]; info.AutoMerge == nil || !*info.AutoMerge {
		t.Error("a task that followed the project should keep merging by itself")
	}
	if info := p.info.Tasks["2"]; info.AutoMerge == nil || *info.AutoMerge {
		t.Error("a task that opted out should stay opted out")
	}
	if p.info.Defaults.Model != DefaultModel {
		t.Error("a project without a default model should offer claude's own")
	}
}
