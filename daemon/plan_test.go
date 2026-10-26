package daemon

import (
	"path/filepath"
	"testing"
)

// A plan holding no work starts from the latest branch; one holding work
// stays where the work was done.
func TestFreshen(t *testing.T) {
	task, origin := testTask(t)
	task.info.Phase = PhasePlan
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	commitUpstream(t, origin, "add b.txt", map[string]string{"b.txt": "two\n"})
	if err := task.freshen(); err != nil {
		t.Fatal(err)
	}
	tip := gitRun(t, origin, "rev-parse", "HEAD")
	if gitRun(t, task.repoDir(), "rev-parse", "HEAD") != tip || gitRun(t, task.repoDir(), "rev-parse", stepsBranch) != tip {
		t.Error("the workspace is not on the latest branch")
	}
	if !exists(filepath.Join(task.repoDir(), "b.txt")) {
		t.Error("the tree is not the branch's")
	}

	commitUpstream(t, origin, "add c.txt", map[string]string{"c.txt": "three\n"})
	writeWork(t, task, "a.txt", "changed\n")
	if err := task.freshen(); err != nil {
		t.Fatal(err)
	}
	if gitRun(t, task.repoDir(), "rev-parse", "HEAD") != tip || readFile(filepath.Join(task.repoDir(), "a.txt")) != "changed\n" {
		t.Error("work was moved")
	}
}

// Leaving a plan: one with nothing in it goes, one with work in it is the
// human's, and one with only a plan written stays a plan.
func TestLeavePlan(t *testing.T) {
	task, _ := testTask(t)
	task.info.Phase = PhasePlan
	task.p.info.Tasks[task.tid] = task.info
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	task.info.Description = "make the widget blue"
	task.leavePlan()
	if task.info.Phase != PhasePlan || task.info.Description != "make the widget blue" {
		t.Errorf("a written plan is in %q, described as %q", task.info.Phase, task.info.Description)
	}
	writeWork(t, task, "a.txt", "changed\n")
	task.leavePlan()
	if task.info.Phase != PhaseHuman {
		t.Errorf("a plan with work in it is in %q", task.info.Phase)
	}

	blank, _ := testTask(t)
	blank.info.Phase = PhasePlan
	blank.p.info.Tasks[blank.tid] = blank.info
	if err := blank.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	blank.leavePlan()
	if blank.p.tasks[blank.tid] != nil {
		t.Error("a plan with nothing in it was kept")
	}
}
