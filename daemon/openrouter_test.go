package daemon

import "testing"

// A request beyond the task's OpenRouter budget waits for the user, and the
// budget they answer with has to make room for all of it.
func TestOpenRouterRequestBeyondBudget(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	task.p.m.orKey = "mgmt"
	budget := 2.0
	task.info.ORBudget, task.info.ORGranted = &budget, 1

	task.requestOpenRouter(3, "")
	if task.info.Phase != PhaseHuman || task.info.ORAsk != 3 {
		t.Fatalf("the request should wait for the user: %s, asking %g", task.info.Phase, task.info.ORAsk)
	}
	if err := task.GrantOpenRouter("3.5"); err == nil {
		t.Error("a budget leaving $2.50 should not grant $3")
	}
	if task.info.ORAsk != 3 {
		t.Error("a refused answer should leave the request standing")
	}
}
