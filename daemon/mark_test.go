package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// transcript writes a claude session transcript with the given lines, as
// claude keeps one: a JSONL file under the config dir's projects directory.
func transcript(t *testing.T, task *Task, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(task.claudeDir(), "projects", "-work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// markTask is testTask with a workspace and a claude dir under it, the state a
// save point is taken in.
func markTask(t *testing.T) (*Task, string) {
	t.Helper()
	task, origin := testTask(t)
	task.p.info.Tasks = map[string]*TaskInfo{task.tid: task.info}
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	return task, origin
}

// marks are the save points the task's chat log holds, in order.
func marks(t *testing.T, task *Task) []*ChatEntry {
	t.Helper()
	var found []*ChatEntry
	for _, line := range strings.Split(readFile(task.chatFile()), "\n") {
		if line == "" {
			continue
		}
		var e ChatEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.Mark != nil {
			point := e
			found = append(found, &point)
		}
	}
	return found
}

// lastMark is the id of the newest save point in the log.
func lastMark(t *testing.T, task *Task) string {
	t.Helper()
	points := marks(t, task)
	if len(points) == 0 {
		t.Fatal("the log holds no save point")
	}
	return points[len(points)-1].ID
}

// A run's changes become a commit, and the point that names it holds that
// commit next to the length of every transcript claude had then.
func TestMarkCommits(t *testing.T) {
	task, _ := markTask(t)
	transcript(t, task, "s.jsonl", "one", "two")
	write(t, filepath.Join(task.repoDir(), "a.txt"), "changed\n")

	task.mark("Agent", "changed a.txt")
	points := marks(t, task)
	if len(points) != 1 {
		t.Fatalf("got %d save points, want 1", len(points))
	}
	if points[0].Text != "Agent: changed a.txt" {
		t.Errorf("point is called %q", points[0].Text)
	}
	if subject, _ := git(task.repoDir(), "log", "-1", "--format=%s"); subject != "Agent: changed a.txt" {
		t.Errorf("the commit is called %q", subject)
	}
	if dirty, _ := dirtyTree(task.repoDir()); dirty {
		t.Error("the tree is still dirty after the point was taken")
	}
	if got := points[0].Mark.Claude["projects/-work/s.jsonl"]; got != 8 {
		t.Errorf("the transcript was measured at %d bytes, want 8", got)
	}

	// A turn that changed nothing is still a point (the conversation moved);
	// a human's nothing is not.
	task.mark("Agent", "looked around")
	task.mark("Human", "")
	if points = marks(t, task); len(points) != 2 {
		t.Fatalf("got %d save points, want 2", len(points))
	}
	if points[1].Mark.Commit != points[0].Mark.Commit {
		t.Error("a run that committed nothing moved the workspace anyway")
	}
}

// Reverting puts back all three: the tree, the log and what claude remembers.
func TestRevert(t *testing.T) {
	task, _ := markTask(t)
	path := transcript(t, task, "s.jsonl", "one", "two")
	write(t, filepath.Join(task.repoDir(), "a.txt"), "first run\n")
	task.mark("Agent", "first")
	id := lastMark(t, task)

	// A second run, on top of it, that the revert has to undo entirely.
	asked := newEntry("user")
	asked.Text = "now do the other thing"
	task.addEntry(asked)
	write(t, filepath.Join(task.repoDir(), "a.txt"), "second run\n")
	write(t, filepath.Join(task.repoDir(), "b.txt"), "new file\n")
	write(t, path, "one\ntwo\nthree\nfour\n")
	transcript(t, task, "later.jsonl", "a session that started after the point")
	task.note("something the agent said")
	task.mark("Agent", "second")

	_, draft, err := task.UsePoint(id, Use{Chat: true, Work: true})
	if err != nil {
		t.Fatal(err)
	}
	if body := readFile(filepath.Join(task.repoDir(), "a.txt")); body != "first run\n" {
		t.Errorf("a.txt is %q after the revert", body)
	}
	if exists(filepath.Join(task.repoDir(), "b.txt")) {
		t.Error("a file the undone run added is still there")
	}
	if body := readFile(path); body != "one\ntwo\n" {
		t.Errorf("the transcript is %q after the revert", body)
	}
	if exists(filepath.Join(task.claudeDir(), "projects", "-work", "later.jsonl")) {
		t.Error("a session that only existed after the point survived the revert")
	}
	if points := marks(t, task); len(points) != 1 || points[0].ID != id {
		t.Errorf("the log was not cut back to the point: %d points left", len(points))
	}
	if strings.Contains(readFile(task.chatFile()), "something the agent said") {
		t.Error("the log still holds what was said after the point")
	}
	if draft != "now do the other thing" {
		t.Errorf("the message that set the undone run off came back as %q", draft)
	}
}

// Forking leaves the original alone and gives the copy the work as one commit.
func TestForkFull(t *testing.T) {
	task, _ := markTask(t)
	task.info.Title = "Do the thing"
	task.info.Description = "the description"
	transcript(t, task, "s.jsonl", "one", "two")
	write(t, filepath.Join(task.repoDir(), "a.txt"), "forked from here\n")
	task.mark("Agent", "did the thing")
	id := lastMark(t, task)
	// Work after the point, which the fork must not have.
	write(t, filepath.Join(task.repoDir(), "c.txt"), "later\n")

	tid, _, err := task.UsePoint(id, Use{Fork: true, Chat: true, Work: true})
	if err != nil {
		t.Fatal(err)
	}
	fork := task.p.tasks[tid]
	if fork == nil {
		t.Fatal("the fork is not in the project")
	}
	if fork.info.Title != "Do the thing #2" {
		t.Errorf("the fork is called %q", fork.info.Title)
	}
	if fork.info.Description != task.info.Description {
		t.Error("the fork did not get the description")
	}
	if body := readFile(filepath.Join(fork.repoDir(), "a.txt")); body != "forked from here\n" {
		t.Errorf("the fork's a.txt is %q", body)
	}
	if exists(filepath.Join(fork.repoDir(), "c.txt")) {
		t.Error("the fork got work from after the point")
	}
	if dirty, _ := dirtyTree(fork.repoDir()); dirty {
		t.Error("the fork's work was not committed")
	}
	if !fork.info.Started {
		t.Error("the fork does not carry the conversation on")
	}
	if body := readFile(filepath.Join(fork.claudeDir(), "projects", "-work", "s.jsonl")); body != "one\ntwo\n" {
		t.Errorf("the fork's transcript is %q", body)
	}
	if len(fork.info.Pending) != 0 {
		t.Error("a fork that lost nothing was told it did")
	}
	// The original is untouched.
	if !exists(filepath.Join(task.repoDir(), "c.txt")) {
		t.Error("forking took work off the original")
	}
}

// The start point, which every task opens on: rewinding to it leaves a task
// with nothing said, nothing remembered and nothing built — and hands the
// description back to the composer, it being a message like any other.
func TestForkFromTheStart(t *testing.T) {
	task, _ := markTask(t)
	task.info.Title = "Do the thing"
	task.mark("Start", "")
	start := lastMark(t, task)
	transcript(t, task, "s.jsonl", "one")
	asked := newEntry("user")
	asked.Text = "please do the thing"
	task.addEntry(asked)
	write(t, filepath.Join(task.repoDir(), "a.txt"), "work\n")
	task.mark("Agent", "did it")

	tid, draft, err := task.UsePoint(start, Use{Fork: true, Chat: true, Work: true})
	if err != nil {
		t.Fatal(err)
	}
	fork := task.p.tasks[tid]
	if fork.info.Started {
		t.Error("the fork carries a session from before there was one")
	}
	if fork.info.Phase != PhaseHuman {
		t.Errorf("the fork waits in %q, want the human", fork.info.Phase)
	}
	if exists(filepath.Join(fork.claudeDir(), "projects", "-work", "s.jsonl")) {
		t.Error("the fork kept a transcript from after the point")
	}
	if body := readFile(filepath.Join(fork.repoDir(), "a.txt")); body != "one\n" {
		t.Errorf("the fork's a.txt is %q, want the branch's own", body)
	}
	if log := readFile(fork.chatFile()); strings.Contains(log, "please do the thing") {
		t.Errorf("the fork's log reaches past the point: %q", log)
	}
	// Both were put back together, so nothing is out of step and the agent has
	// nothing to be told.
	if len(fork.info.Pending) != 0 {
		t.Errorf("the fork's agent was told about a rewind it cannot notice: %+v", fork.info.Pending)
	}
	if draft != "please do the thing" {
		t.Errorf("the first message did not come back to the composer: %q", draft)
	}
}

// Forks number themselves apart, and forking a fork numbers from the same stem.
func TestForkTitle(t *testing.T) {
	task, _ := markTask(t)
	task.p.info.Tasks = map[string]*TaskInfo{
		"1": {Title: "Do the thing"},
		"2": {Title: "Do the thing #2"},
		"3": {Title: "Something else"},
	}
	for _, from := range []string{"Do the thing", "Do the thing #2"} {
		if got := task.p.forkTitleL(from); got != "Do the thing #3" {
			t.Errorf("forking %q named it %q", from, got)
		}
	}
	if got := task.p.forkTitleL("Something else"); got != "Something else #2" {
		t.Errorf("forking a task nothing follows named it %q", got)
	}
}

// A verdict says what the turn changed, beside where the task goes.
func TestParseDoneChanges(t *testing.T) {
	_, done, bad := parseDone(`Did it.

TPS-DONE: {"next": "user", "changes": "Read the config at startup"}`)
	if done == nil {
		t.Fatalf("the line was not read: %s", bad)
	}
	if done.Changes != "Read the config at startup" {
		t.Errorf("changes is %q", done.Changes)
	}
}

// The commits a task's runs leave behind stay its own: merging still puts one
// commit on the branch, whatever a task committed along the way.
func TestMergeFoldsMarkCommits(t *testing.T) {
	task, origin := markTask(t)
	task.info.Title = "Do the thing"
	write(t, filepath.Join(task.repoDir(), "a.txt"), "first run\n")
	task.mark("Agent", "first")
	write(t, filepath.Join(task.repoDir(), "b.txt"), "second run\n")
	task.mark("Agent", "second")

	before, err := git(origin, "rev-list", "--count", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Merge("the whole task"); err != nil {
		t.Fatal(err)
	}
	after, err := git(origin, "rev-list", "--count", "main")
	if err != nil {
		t.Fatal(err)
	}
	if n, m := atoi(t, before), atoi(t, after); m != n+1 {
		t.Errorf("merging added %d commits to the branch, want 1", m-n)
	}
	if subject, _ := git(origin, "log", "-1", "--format=%s"); subject != "the whole task" {
		t.Errorf("the branch's commit is called %q", subject)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if out, _ := git(origin, "show", "main:"+name); out == "" {
			t.Errorf("%s did not make it onto the branch", name)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The two rewinds are separate: putting the conversation back to the start
// while leaving the tree alone is a new agent on work that is already there,
// which is the one thing it has to be told.
func TestRewindTheConversationOnly(t *testing.T) {
	task, _ := markTask(t)
	task.info.Title = "Do the thing"
	task.info.Description = "make the widget blue"
	task.mark("Start", "")
	start := lastMark(t, task)
	path := transcript(t, task, "s.jsonl", "one", "two")
	asked := newEntry("user")
	asked.Text = "please do the thing"
	task.addEntry(asked)
	write(t, filepath.Join(task.repoDir(), "a.txt"), "the work\n")
	task.mark("Agent", "did it")

	if _, _, err := task.UsePoint(start, Use{Chat: true}); err != nil {
		t.Fatal(err)
	}
	if body := readFile(filepath.Join(task.repoDir(), "a.txt")); body != "the work\n" {
		t.Errorf("a.txt is %q; the tree was to be left alone", body)
	}
	if exists(path) {
		t.Error("claude's transcript survived a rewind to before it existed")
	}
	if task.info.Started {
		t.Error("the task still claims a session to carry on")
	}
	if log := readFile(task.chatFile()); strings.Contains(log, "please do the thing") {
		t.Errorf("the log reaches past the point: %q", log)
	}
	if task.info.Phase != PhaseHuman {
		t.Errorf("the task waits in %q, want the human", task.info.Phase)
	}
	if len(task.info.Pending) != 1 || !strings.Contains(task.info.Pending[0].Text, "/work holds work") {
		t.Errorf("the agent was not told the tree is ahead of it: %+v", task.info.Pending)
	}
	// The task is the same task: its own description is not the agent's to see,
	// but it is what the board calls it.
	if task.info.Description == "" {
		t.Error("rewinding took the task's own description with it")
	}
}

// And the other way round: the tree goes back, the conversation stays whole.
func TestRevertTheCodeOnly(t *testing.T) {
	task, _ := markTask(t)
	task.info.Started = true
	transcript(t, task, "s.jsonl", "one")
	write(t, filepath.Join(task.repoDir(), "a.txt"), "first\n")
	task.mark("Agent", "first")
	id := lastMark(t, task)
	write(t, filepath.Join(task.repoDir(), "a.txt"), "second\n")
	asked := newEntry("user")
	asked.Text = "and now the other thing"
	task.addEntry(asked)
	task.mark("Agent", "second")

	if _, _, err := task.UsePoint(id, Use{Work: true}); err != nil {
		t.Fatal(err)
	}
	if body := readFile(filepath.Join(task.repoDir(), "a.txt")); body != "first\n" {
		t.Errorf("a.txt is %q; the tree was to go back", body)
	}
	if log := readFile(task.chatFile()); !strings.Contains(log, "and now the other thing") {
		t.Error("the log was cut, though only the code was reverted")
	}
	if len(marks(t, task)) != 2 {
		t.Error("the log lost save points, though only the code was reverted")
	}
	if !task.info.Started {
		t.Error("the task forgot its session, though only the code was reverted")
	}
	if len(task.info.Pending) != 1 || !strings.Contains(task.info.Pending[0].Text, "no longer holds") {
		t.Errorf("the agent was not told the tree went back under it: %+v", task.info.Pending)
	}
}

// A point from before the agent ever ran has no transcript behind it, so there
// is no conversation to carry on however the dashboard asks: resuming a
// session that is not there is how claude refuses to start at all.
func TestPointWithoutASession(t *testing.T) {
	task, _ := markTask(t)
	write(t, filepath.Join(task.repoDir(), "a.txt"), "what the human wrote\n")
	task.mark("Human", "")
	points := marks(t, task)
	if len(points) != 1 || len(points[0].Mark.Claude) != 0 {
		t.Fatalf("the point should hold no transcripts: %+v", points)
	}

	tid, _, err := task.UsePoint(points[0].ID, Use{Fork: true, Chat: true, Work: true})
	if err != nil {
		t.Fatal(err)
	}
	fork := task.p.tasks[tid]
	if fork.info.Started {
		t.Error("the fork would resume a session that never existed")
	}
	// Both were put back to the same point, so nothing is out of step and there is
	// nothing for the agent to be told.
	if len(fork.info.Pending) != 0 {
		t.Errorf("the fork's agent was told about a rewind it cannot notice: %+v", fork.info.Pending)
	}
	if body := readFile(filepath.Join(fork.repoDir(), "a.txt")); body != "what the human wrote\n" {
		t.Errorf("the fork's a.txt is %q; the work was to come along", body)
	}
}
