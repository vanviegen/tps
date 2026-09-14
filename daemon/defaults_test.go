package daemon

import (
	"testing"

	"github.com/vanviegen/tps/hub"
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
		Defaults: TaskDefaults{Model: "opus", ReviewModel: "haiku", OnReady: AnswerReview, ReviewLoops: 2, Budget: &budget}})
	m.projects[p.pid] = p

	tid, err := p.CreateTask(map[string]any{"description": "do it"})
	if err != nil {
		t.Fatal(err)
	}
	info := p.info.Tasks[tid]
	if info.Model != "opus" || info.Budget == nil || *info.Budget != 5 {
		t.Errorf("defaults not copied: %+v", info)
	}
	if info.ReviewModel != "haiku" || info.OnReady != AnswerReview || info.ReviewLoops != 2 {
		t.Errorf("the review settings are defaults too: %+v", info)
	}
	// The copy is the task's own: raising its budget is not raising the default.
	*info.Budget = 99
	if *p.info.Defaults.Budget != 5 {
		t.Error("the default budget was changed along with the task's")
	}

	tid, err = p.CreateTask(map[string]any{"description": "do it my way", "model": "haiku", "budget": "1.5", "onReady": "merge", "reviewLoops": "1"})
	if err != nil {
		t.Fatal(err)
	}
	info = p.info.Tasks[tid]
	if info.Model != "haiku" || info.Budget == nil || *info.Budget != 1.5 || info.OnReady != AnswerMerge || info.ReviewLoops != 1 {
		t.Errorf("the task's own settings should win: %+v", info)
	}
}

// A project takes what it was not given: claude's own models, and a colour.
func TestProjectFallbacks(t *testing.T) {
	p := newProject(testManager(), "project", &ProjectInfo{Dir: "/tmp/project"})
	if p.info.Defaults.Model != DefaultModel || p.info.Defaults.ReviewModel != DefaultModel {
		t.Errorf("a project without default models should offer claude's own: %+v", p.info.Defaults)
	}
	if !colorRe.MatchString(p.info.Color) {
		t.Errorf("a project should be given a colour: %q", p.info.Color)
	}
}
