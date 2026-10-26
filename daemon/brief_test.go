package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// The agent hears of the brief in full the first time, as a diff after, and
// not at all while it is what it was last told; the log keeps what it told,
// which is what a rewind goes back to.
func TestBriefing(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	if task.briefing() != nil {
		t.Error("a workspace without a brief told the agent of one")
	}
	writeWork(t, task, briefFile, "Make the widget blue.\n\nAnd round.\n")
	first := task.briefing()
	if first == nil || first.Brief == nil || !strings.HasSuffix(first.Text, "Make the widget blue.\n\nAnd round.") {
		t.Fatalf("the first briefing is %+v", first)
	}
	task.appendChat(first)
	if task.briefing() != nil {
		t.Error("an unchanged brief was told again")
	}
	writeWork(t, task, briefFile, "Make the widget green.\n\nAnd round.\n")
	second := task.briefing()
	if second == nil || !strings.Contains(second.Text, "-Make the widget blue.\n+Make the widget green.") || strings.Contains(second.Text, "diff --git") {
		t.Fatalf("the change was told as %+v", second)
	}
	if task.info.Briefed != "Make the widget green.\n\nAnd round." {
		t.Errorf("the brief the agent has is %q", task.info.Briefed)
	}
	task.appendChat(second)
	log := []byte(readFile(task.chatFile()))
	if got := lastBrief(log); got != task.info.Briefed {
		t.Errorf("the log last told of %q", got)
	}
	if got := lastBrief(log[:strings.Index(string(log), "\n")+1]); got != "Make the widget blue.\n\nAnd round." {
		t.Errorf("the log up to the first briefing told of %q", got)
	}
	if nextPrompt(log) != "" {
		t.Error("a briefing was handed back to the composer as a message")
	}
}

// A workspace made for a task gets its brief written into it, and what lands
// leaves the brief out.
func TestBriefIsWrittenAndNotMerged(t *testing.T) {
	task, _ := testTask(t)
	task.info.Description = "make the widget blue"
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	if body := readFile(filepath.Join(task.repoDir(), briefFile)); body != "make the widget blue\n" {
		t.Errorf("the brief file holds %q", body)
	}
	tree, err := landingTree(task.repoDir())
	if err != nil {
		t.Fatal(err)
	}
	if tree != gitRun(t, task.repoDir(), "rev-parse", "HEAD^{tree}") {
		t.Error("the brief would land with the work")
	}
}

// A plan holding nothing but its brief starts from the latest branch; one
// holding work stays where the work was done.
func TestFreshen(t *testing.T) {
	task, origin := testTask(t)
	task.info.Phase = PhasePlan
	task.info.Description = "make the widget blue"
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
	if !exists(filepath.Join(task.repoDir(), "b.txt")) || readFile(filepath.Join(task.repoDir(), briefFile)) != "make the widget blue\n" {
		t.Error("the tree is not the branch's with the brief on top")
	}

	commitUpstream(t, origin, "add c.txt", map[string]string{"c.txt": "three\n"})
	writeWork(t, task, "a.txt", "changed\n")
	if err := task.freshen(); err != nil {
		t.Fatal(err)
	}
	if gitRun(t, task.repoDir(), "rev-parse", "HEAD") != tip || readFile(filepath.Join(task.repoDir(), "a.txt")) != "changed\n" {
		t.Error("work besides the brief was moved")
	}
}

// Leaving a plan: one with nothing in it goes, one with work in it is the
// human's, and one with only a brief stays a plan.
func TestLeavePlan(t *testing.T) {
	task, _ := testTask(t)
	task.info.Phase = PhasePlan
	task.p.info.Tasks[task.tid] = task.info
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, briefFile, "make the widget blue\n")
	task.leavePlan()
	if task.info.Phase != PhasePlan || task.info.Description != "make the widget blue" {
		t.Errorf("a plan with a brief is in %q, described as %q", task.info.Phase, task.info.Description)
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
