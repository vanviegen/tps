package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// Tasks merged while the train is busy go in one batch, each its own commit.
func TestTrainTakesABatch(t *testing.T) {
	first, origin := testTask(t)
	p := first.p
	second := newTask(p, "2", &TaskInfo{Phase: PhaseAgent, Title: "second"})
	p.tasks[second.tid] = second
	p.training = true // busy: nothing lands until both wait
	for _, task := range []*Task{first, second} {
		if err := task.ensureWorkspace(); err != nil {
			t.Fatal(err)
		}
		writeWork(t, task, task.tid+".txt", "work of "+task.tid+"\n")
		if err := task.Merge("task " + task.tid); err != nil {
			t.Fatal(err)
		}
	}
	p.training = false
	p.startTrain()
	waitLanded(t, first)
	waitLanded(t, second)
	if got := gitRun(t, origin, "log", "--format=%s", "-3"); got != "task 2\ntask 1\nadd a.txt" && got != "task 1\ntask 2\nadd a.txt" {
		t.Errorf("history: %q", got)
	}
	for _, task := range []*Task{first, second} {
		if task.info.Phase != PhaseDone || !strings.Contains(gitRun(t, origin, "log", "--format=%B", "-2"), taskTrailer+": "+task.info.Stamp) {
			t.Errorf("task %s: %s, stamped %q", task.tid, task.info.Phase, task.info.Stamp)
		}
	}
	if p.tasks[trainTid] == nil {
		t.Error("the train has no task to show for it")
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
