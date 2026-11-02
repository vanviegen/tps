package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A schedule comes up once on each day it is set to, from its time on.
func TestScheduleDue(t *testing.T) {
	monday := time.Date(2026, 11, 2, 9, 30, 0, 0, time.Local)
	s := &Schedule{Enabled: true, Time: "09:00"}
	s.Days[time.Monday] = true
	if !s.due(monday) {
		t.Error("not due past its time on its day")
	}
	if s.due(monday.Add(-time.Hour)) || s.due(monday.AddDate(0, 0, 1)) {
		t.Error("due before its time, or on another day")
	}
	s.LastRun = "2026-11-02"
	if s.due(monday) || !s.due(monday.AddDate(0, 0, 7)) {
		t.Error("due twice a day, or not again a week later")
	}
	s.LastRun, s.Enabled = "", false
	if s.due(monday) {
		t.Error("due while disabled")
	}
}

// A scheduled task is made reviewed-and-merged, its runs start as tasks of
// their own, and one is skipped while the previous one is still at work.
func TestScheduledRuns(t *testing.T) {
	task, origin := testTask(t)
	p := task.p
	tid, err := p.CreateTask(map[string]any{"schedule": map[string]any{}, "draft": "tidy up"})
	if err != nil {
		t.Fatal(err)
	}
	sched := p.tasks[tid]
	info := sched.info
	if info.Schedule == nil || info.OnReady != AnswerReview || info.OnAccept != AnswerMerge || reviewLoops(info.ReviewLoops) != maxReviewLoops {
		t.Fatalf("a scheduled task's defaults: %+v", info)
	}
	if err := sched.MoveTo(PhaseAgent); err != errScheduled {
		t.Errorf("a scheduled task was moved: %v", err)
	}
	if err := os.RemoveAll(origin); err != nil { // the run's workspace fails, rather than an agent being started
		t.Fatal(err)
	}
	info.Schedule.LastRun = ""
	now := time.Now()
	info.Schedule.Time = now.Format("15:04")
	p.m.mu.Lock()
	p.runSchedulesL(now)
	var run *Task
	for _, other := range p.tasks {
		if other.info.Scheduled == tid {
			run = other
		}
	}
	p.m.mu.Unlock()
	if run == nil {
		t.Fatal("no run was started")
	}
	if want := info.Title + " " + now.Format(time.DateOnly); run.info.Title != want || run.info.Description != "tidy up" || run.info.OnAccept != AnswerMerge {
		t.Errorf("the run is %+v, not titled %q", run.info, want)
	}
	run.starting.Lock() // its workspace was tried
	run.starting.Unlock()

	p.m.mu.Lock()
	run.info.Phase = PhaseAgent
	info.Schedule.LastRun = ""
	p.runSchedulesL(now)
	p.m.mu.Unlock()
	if !strings.Contains(sched.lastNote(), "still at work") {
		t.Errorf("a run beside one at work was not skipped: %q", sched.lastNote())
	}
}

// A scheduled run that reaches its budget gets a few more dollars to wrap up
// in, and then no more.
func TestOverrun(t *testing.T) {
	task, _ := testTask(t)
	budget := 1.0
	task.info.Budget, task.info.Spent, task.info.Scheduled = &budget, 1, "2"
	task.lock()
	defer task.unlock()
	for range maxOverruns {
		if !task.overrunL() {
			t.Fatal("a scheduled run was not given more")
		}
	}
	if task.overrunL() || *task.info.Budget != 1+maxOverruns*overrunStep {
		t.Errorf("overran to a budget of $%g", *task.info.Budget)
	}
}

// A scheduled run's agent gets a person only by asking for one with the file;
// ending its turns with the work not ready otherwise
// sends it back in, a few times in a row at most.
func TestUnattendedTurns(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(task.repoDir(), humanRequiredFile)
	writeWork(t, task, humanRequiredFile, "")
	if task.humanRequired() {
		t.Error("a task nobody scheduled asked for a person")
	}
	task.info.Scheduled = "2"
	if !task.humanRequired() || !exists(file) {
		t.Error("the request for a person was not seen, or not left for the turn that acts on it")
	}
	task.lock()
	defer task.unlock()
	for range maxReadyNudges {
		if !task.readyNudgeL() {
			t.Fatal("the agent was not sent back in")
		}
	}
	if task.readyNudgeL() {
		t.Error("the agent was sent back in without end")
	}
}
