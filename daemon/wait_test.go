package daemon

import (
	"strings"
	"testing"
	"time"
)

// task graph helper: tasks by tid, each with a phase and what it waits for.
func testProject(infos map[string]*TaskInfo) *Project {
	p := &Project{tasks: map[string]*Task{}}
	for tid, info := range infos {
		p.tasks[tid] = &Task{p: p, tid: tid, info: info}
	}
	return p
}

func TestBlocked(t *testing.T) {
	p := testProject(map[string]*TaskInfo{
		"1": {Phase: PhaseMerged},
		"2": {Phase: PhaseAgent},
		"3": {Phase: PhasePlan, WaitFor: []string{"1", "2", "9"}}, // 9 was deleted
	})
	if !p.tasks["3"].blockedL() {
		t.Error("2 is not done yet")
	}
	p.tasks["2"].info.Phase = PhaseMerged
	if p.tasks["3"].blockedL() {
		t.Error("a deleted task is nothing to wait for")
	}
}

func TestWaitOver(t *testing.T) {
	p := testProject(map[string]*TaskInfo{
		"1": {Phase: PhaseAgent},
		"2": {Phase: PhaseHuman, WaitFor: []string{"1"}, Deferred: "go on"},
	})
	task := p.tasks["2"]
	if task.waitOverL() || !task.waitingL() {
		t.Fatal("1 is not done yet: the task is held")
	}
	p.tasks["1"].info.Phase = PhaseClosed
	if !task.waitOverL() {
		t.Fatal("1 is done: the wait is over")
	}
	task.ending = true
	if task.waitOverL() || !task.waitingL() {
		t.Error("a wait being ended is held, and not ended twice")
	}
	task.ending = false
	// Taken off by hand, the wait is over for a message held back alone.
	task.info.WaitFor = nil
	if !task.waitOverL() {
		t.Error("a message held back with nothing to wait for should go")
	}
	task.info.Deferred = ""
	if task.waitOverL() || task.waitingL() {
		t.Error("a task with no wait and no message has nothing to end")
	}
}

func TestWaitsFor(t *testing.T) {
	p := testProject(map[string]*TaskInfo{
		"1": {Phase: PhasePlan},
		"2": {Phase: PhasePlan, WaitFor: []string{"1"}},
		"3": {Phase: PhasePlan, WaitFor: []string{"2"}},
	})
	if !p.waitsForL("3", "1", nil) {
		t.Error("3 waits for 1 through 2")
	}
	if p.waitsForL("1", "3", nil) {
		t.Error("1 does not wait for 3")
	}
	// A cycle in the stored data must not hang the check.
	p.tasks["1"].info.WaitFor = []string{"3"}
	if !p.waitsForL("1", "2", nil) {
		t.Error("1 waits for 2 the long way round")
	}
}

func TestJoinMessages(t *testing.T) {
	if got := joinMessages("one", " ", "two\n"); got != "one\n\ntwo" {
		t.Errorf("got %q", got)
	}
}

// A task set to wait holds back what the user says. Once the task it waits
// for is done, it is rebased onto the branch that one's work landed on, and
// the agent is sent in with all that was held back.
func TestWaitHoldsMessagesAndRebases(t *testing.T) {
	task, origin := markTask(t)
	task.info.Phase = PhaseHuman
	dep := newTask(task.p, "2", &TaskInfo{Phase: PhaseHuman})
	task.p.tasks[dep.tid], task.p.info.Tasks[dep.tid] = dep, dep.info
	if err := task.Update(map[string]any{"waitFor": []any{dep.tid}}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two"} {
		if err := task.SendChat(text); err != nil {
			t.Fatal(err)
		}
	}
	if task.info.Deferred != "one\n\ntwo" || strings.Contains(readFile(task.chatFile()), `"k":"user"`) {
		t.Fatalf("not held back: %q", task.info.Deferred)
	}
	commitUpstream(t, origin, "the other task's work", map[string]string{"b.txt": "two\n"})
	dep.lock()
	dep.setPhaseL(PhaseMerged)
	dep.unlock()
	for i := 0; !strings.Contains(readFile(task.chatFile()), `"k":"user"`); i++ {
		if i == 100 {
			t.Fatal("the agent was never sent in")
		}
		time.Sleep(100 * time.Millisecond)
	}
	task.lock()
	waitFor, deferred := task.info.WaitFor, task.info.Deferred
	task.unlock()
	if waitFor != nil || deferred != "" {
		t.Errorf("the wait is not over: %v, %q", waitFor, deferred)
	}
	if !strings.Contains(readFile(task.chatFile()), `"text":"one\n\ntwo"`) {
		t.Error("the agent was not sent in with what was held back")
	}
	if gitRun(t, task.repoDir(), "rev-parse", "HEAD") != gitRun(t, origin, "rev-parse", "HEAD") {
		t.Error("the workspace was not rebased onto the branch")
	}
}
