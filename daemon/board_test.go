package daemon

import (
	"strings"
	"testing"
	"time"
)

// The tool's requests are answered by the daemon on its tick: here a loop
// stands in for that tick, and the tool runs against the task's services dir.
func TestBoardRequests(t *testing.T) {
	task, _ := testTask(t)
	task.info.Title = "Use the fix"
	task.p.info.Name, task.p.info.NextTask = "app", 2
	task.p.info.Tasks[task.tid] = task.info
	boardPoll = 50 * time.Millisecond
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
			task.lock()
			task.checkBoardRequestsL()
			task.unlock()
		}
	}()
	st := &guestTool{root: task.servicesDir()}

	ans, err := st.askBoard(BoardRequest{Cmd: "projects"})
	if err != nil || !strings.Contains(ans.Text, "app ") || !strings.Contains(ans.Text, "(this task's)") {
		t.Errorf("projects: %q, %v", ans.Text, err)
	}
	if code := st.createTask([]string{"nope", "Fix it", "Do"}); code != 2 {
		t.Errorf("create-task in no project: %d", code)
	}
	if code := st.createTask([]string{".", "Fix it", "Do"}); code != 0 {
		t.Fatalf("create-task: %d", code)
	}
	made := task.p.tasks["2"]
	if made == nil || made.info.Phase != PhasePlan || made.info.Requested != "Use the fix" || made.info.Description != "Do" {
		t.Fatalf("the task made: %+v", made)
	}
	if e, ok := task.p.m.hub.LastChat(task.key()); !ok || e.K != "task" {
		t.Errorf("the log ends in %+v", e)
	}

	setPhase := func(phase Phase, after time.Duration) {
		time.AfterFunc(after, func() {
			task.lock()
			made.info.Phase = phase
			task.unlock()
		})
	}
	if code := st.awaitTask([]string{"./2", "merged", "0"}); code != 124 {
		t.Errorf("await a plan's merge: %d", code)
	}
	setPhase(PhaseAgent, 100*time.Millisecond)
	if code := st.awaitTask([]string{"./2", "5"}); code != 0 {
		t.Errorf("await a change: %d", code)
	}
	setPhase(PhaseDone, 100*time.Millisecond)
	if code := st.awaitTask([]string{"app/2", "merged", "5"}); code != 0 {
		t.Errorf("await the merge: %d", code)
	}
	if code := st.awaitTask([]string{"./2", "deployed", "5"}); code != 1 {
		t.Errorf("await a deploy of a project that does not deploy: %d", code)
	}
	setPhase(PhaseClosed, 0)
	if code := st.awaitTask([]string{"./2", "merged", "5"}); code != 1 {
		t.Errorf("await the merge of a closed task: %d", code)
	}
	if code := st.awaitTask([]string{"./9", "merged", "5"}); code != 1 {
		t.Errorf("await a task that is not there: %d", code)
	}
	if code := st.awaitTask([]string{"./2", "soon"}); code != 2 {
		t.Errorf("a state that is none: %d", code)
	}
}
