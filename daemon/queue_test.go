package daemon

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tasks merged one after the other land in that order, each its own commit.
func TestQueueLandsInTurn(t *testing.T) {
	first, origin := testTask(t)
	p := first.p
	second := newTask(p, "2", &TaskInfo{Phase: PhaseAgent, Title: "second"})
	p.tasks[second.tid] = second
	for _, task := range []*Task{first, second} {
		if err := task.ensureWorkspace(); err != nil {
			t.Fatal(err)
		}
		writeWork(t, task, task.tid+".txt", "work of "+task.tid+"\n")
		if err := task.Merge("task "+task.tid, false); err != nil {
			t.Fatal(err)
		}
	}
	waitLanded(t, first)
	waitLanded(t, second)
	if got := gitRun(t, origin, "log", "--format=%s", "-3"); got != "task 2\ntask 1\nadd a.txt" {
		t.Errorf("history: %q", got)
	}
	for _, task := range []*Task{first, second} {
		if task.info.Phase != PhaseDone || !strings.Contains(gitRun(t, origin, "log", "--format=%B", "-2"), taskTrailer+": "+task.info.Stamp) {
			t.Errorf("task %s: %s, stamped %q", task.tid, task.info.Phase, task.info.Stamp)
		}
	}
	if len(p.info.Queue) > 0 {
		t.Errorf("queue left: %v", p.info.Queue)
	}
}

// A task moved ahead of the one merging takes its place, which waits its
// turn again.
func TestMoveInQueue(t *testing.T) {
	first, _ := testTask(t)
	p := first.p
	for _, tid := range []string{"2", "3"} {
		p.tasks[tid] = newTask(p, tid, &TaskInfo{Phase: PhaseMerge})
	}
	for _, task := range p.tasks {
		task.merging = true // under way already: nothing starts
	}
	p.info.Queue = []string{"1", "2", "3"}
	if err := p.MoveInQueue("3", 0); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.info.Queue, []string{"3", "1", "2"}) {
		t.Errorf("queue: %v", p.info.Queue)
	}
	if err := p.MoveInQueue("3", 9); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.info.Queue, []string{"1", "2", "3"}) {
		t.Errorf("queue: %v", p.info.Queue)
	}
	if err := p.MoveInQueue("4", 0); err == nil {
		t.Error("moved a task that is not queued")
	}
}

// The board's branch: the tasks' commits found by their stamps, the user's
// own commits between them, and the deploy where it is — or, once the branch
// was rewritten under it, after the newest task it held.
func TestBranchState(t *testing.T) {
	first, origin := testTask(t)
	p := first.p
	p.deploy = "./ship"
	second := newTask(p, "2", &TaskInfo{Phase: PhaseAgent, Title: "second"})
	p.tasks[second.tid] = second
	if err := first.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, first, "1.txt", "work of 1\n")
	mergeAndLand(t, first, "task 1")
	commitUpstream(t, origin, "by hand", map[string]string{"a.txt": "by hand\n"})
	if err := second.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, second, "2.txt", "work of 2\n")
	mergeAndLand(t, second, "task 2")

	subjects := func(log []branchCommit) string {
		var out []string
		for _, c := range log {
			out = append(out, c.Subject+"/"+c.Tid)
		}
		return strings.Join(out, " ")
	}
	p.deployed(gitRun(t, origin, "rev-parse", "main~1"))
	log, at, rewritten := p.branchState()
	if got := subjects(log); got != "task 2/2 by hand/ task 1/1" {
		t.Errorf("the branch: %s", got)
	}
	if at != log[1].Sha || rewritten {
		t.Errorf("the deploy at %s (rewritten %v), want the commit by hand", at, rewritten)
	}

	// Amending the tip rewrites the branch under a deploy of it: the deploy
	// is found again by the task it held.
	p.deployed(gitRun(t, origin, "rev-parse", "main"))
	write(t, filepath.Join(origin, "2.txt"), "work of 2, amended\n")
	gitRun(t, origin, "commit", "--quiet", "--all", "--amend", "--no-edit")
	log, at, rewritten = p.branchState()
	if !rewritten || at != log[0].Sha || log[0].Tid != "2" {
		t.Errorf("after a rewrite: at %s (rewritten %v), the tip %v", at, rewritten, log[0])
	}
	if err := p.Deploy(gitRun(t, origin, "rev-parse", "ORIG_HEAD")); err == nil {
		t.Error("deploying a commit the rewrite left behind")
	}
}

// A merge that would overwrite uncommitted changes in the project's checkout
// waits at the head of the queue, and lands once they are gone.
func TestMergeWaitsForTheCheckout(t *testing.T) {
	task, origin := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, "a.txt", "one task\n")
	write(t, filepath.Join(origin, "a.txt"), "one by hand\n")
	if err := task.Merge("the task", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		task.lock()
		held := task.held
		task.unlock()
		if held != "" {
			break
		}
		if i == 100 {
			t.Fatal("the merge was not held back")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if task.info.Phase != PhaseMerge || !slices.Equal(task.p.info.Queue, []string{"1"}) {
		t.Errorf("phase %s, queue %v", task.info.Phase, task.p.info.Queue)
	}
	gitRun(t, origin, "checkout", "--", "a.txt")
	task.p.retryHeld()
	waitLanded(t, task)
	if got := readFile(filepath.Join(origin, "a.txt")); task.info.Phase != PhaseDone || got != "one task\n" {
		t.Errorf("phase %s, a.txt %q", task.info.Phase, got)
	}
}
