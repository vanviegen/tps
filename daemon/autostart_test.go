package daemon

import "testing"

// task graph helper: tasks by tid, each with a phase and what it follows.
func testProject(infos map[string]*TaskInfo) *Project {
	p := &Project{tasks: map[string]*Task{}}
	for tid, info := range infos {
		p.tasks[tid] = &Task{p: p, tid: tid, info: info}
	}
	return p
}

func TestBlocked(t *testing.T) {
	p := testProject(map[string]*TaskInfo{
		"1": {Phase: PhaseDone},
		"2": {Phase: PhaseAgent},
		"3": {Phase: PhasePlan, StartAfter: []string{"1", "2", "9"}}, // 9 was deleted
	})
	if !p.tasks["3"].blockedL() {
		t.Error("2 is not done yet")
	}
	p.tasks["2"].info.Phase = PhaseDone
	if p.tasks["3"].blockedL() {
		t.Error("a deleted task is nothing to wait for")
	}
}

func TestAutoStartable(t *testing.T) {
	p := testProject(map[string]*TaskInfo{
		"1": {Phase: PhaseDone},
		"2": {Phase: PhasePlan, Description: "do it", StartAfter: []string{"1"}},
	})
	task := p.tasks["2"]
	if !task.autoStartableL() {
		t.Fatal("a plan task whose tasks are all done should start")
	}
	for _, c := range []struct {
		name   string
		break_ func()
		undo   func()
	}{
		{"an open plan", func() { task.viewers = 1 }, func() { task.viewers = 0 }},
		{"an empty description", func() { task.info.Description = " " }, func() { task.info.Description = "do it" }},
		{"nothing to follow", func() { task.info.StartAfter = nil }, func() { task.info.StartAfter = []string{"1"} }},
		{"a start already under way", func() { task.autoStarting = true }, func() { task.autoStarting = false }},
		{"a task not in plan", func() { task.info.Phase = PhaseHuman }, func() { task.info.Phase = PhasePlan }},
		{"an unfinished task", func() { p.tasks["1"].info.Phase = PhaseAgent }, func() { p.tasks["1"].info.Phase = PhaseDone }},
	} {
		c.break_()
		if task.autoStartableL() {
			t.Errorf("%s should hold the task back", c.name)
		}
		c.undo()
	}
	if !task.autoStartableL() {
		t.Error("the task should be startable again")
	}
}

func TestStartsAfter(t *testing.T) {
	p := testProject(map[string]*TaskInfo{
		"1": {Phase: PhasePlan},
		"2": {Phase: PhasePlan, StartAfter: []string{"1"}},
		"3": {Phase: PhasePlan, StartAfter: []string{"2"}},
	})
	if !p.startsAfterL("3", "1", nil) {
		t.Error("3 follows 1 through 2")
	}
	if p.startsAfterL("1", "3", nil) {
		t.Error("1 does not follow 3")
	}
	// A cycle in the stored data must not hang the check.
	p.tasks["1"].info.StartAfter = []string{"3"}
	if !p.startsAfterL("1", "2", nil) {
		t.Error("1 follows 2 the long way round")
	}
}
