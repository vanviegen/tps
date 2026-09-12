package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vanviegen/tps/hub"
)

// What a merged task keeps and what it loses: the conversation stays, the
// clone goes, and picking the task up gives it the branch as it stands then.
func TestDropAndFreshClone(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "project")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(repo string, args ...string) string {
		out, err := git(repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	commit := func(name, text string) {
		if err := os.WriteFile(filepath.Join(origin, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		run(origin, "add", "-A")
		run(origin, "commit", "--quiet", "-m", "add "+name)
	}
	run(origin, "init", "--quiet", "-b", "main")
	run(origin, "config", "user.name", "TPS")
	run(origin, "config", "user.email", "tps@localhost")
	commit("a.txt", "one\n")

	m := &Manager{projects: map[string]*Project{}, dataDir: filepath.Join(dir, "data"), hub: hub.New(nil)}
	p := &Project{m: m, pid: "project", info: &ProjectInfo{Dir: origin}, tasks: map[string]*Task{}, defaultBranch: "main"}
	m.projects[p.pid] = p
	task := newTask(p, "1", &TaskInfo{Phase: PhaseAgent})
	p.tasks[task.tid] = task

	if err := task.ensureClone(); err != nil {
		t.Fatal(err)
	}
	task.note("hello") // the chat log lives beside the clone, not in it
	leftover := filepath.Join(task.repoDir(), "scratch.txt")
	if err := os.WriteFile(leftover, []byte("never committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Merged: the tree goes, what the task knows stays.
	if err := task.dropWorkspace(); err != nil {
		t.Fatal(err)
	}
	if exists(task.repoDir()) {
		t.Error("the clone survived the merge")
	}
	if !exists(task.claudeDir()) || !exists(task.chatFile()) {
		t.Error("the conversation did not survive the merge")
	}

	// Picked up again, after another task merged something of its own.
	commit("b.txt", "two\n")
	if err := task.freshClone(); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(task.repoDir(), "b.txt")) {
		t.Error("the new clone misses what landed on the branch since")
	}
	if exists(leftover) {
		t.Error("the old workspace came back with it")
	}
	if run(task.repoDir(), "rev-parse", "HEAD") != run(origin, "rev-parse", "HEAD") {
		t.Error("the new clone is not at the branch tip")
	}
	if !exists(task.chatFile()) {
		t.Error("the chat log did not survive picking the task up")
	}
}

// A tree the daemon cannot unlink every file of — what a container writing as
// root leaves behind — must not hold the task hostage: the workspace path is
// free, so picking the task up clones into it as usual.
func TestDropWorkspaceKeepsGoingOnLeftovers(t *testing.T) {
	dir := t.TempDir()
	origin := filepath.Join(dir, "project")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet", "-b", "main"}, {"config", "user.name", "TPS"},
		{"config", "user.email", "tps@localhost"}, {"commit", "--quiet", "--allow-empty", "-m", "root"}} {
		if _, err := git(origin, args...); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{projects: map[string]*Project{}, dataDir: filepath.Join(dir, "data"), hub: hub.New(nil)}
	p := &Project{m: m, pid: "project", info: &ProjectInfo{Dir: origin}, tasks: map[string]*Task{}, defaultBranch: "main"}
	m.projects[p.pid] = p
	task := newTask(p, "1", &TaskInfo{Phase: PhaseAgent})
	p.tasks[task.tid] = task
	if err := task.ensureClone(); err != nil {
		t.Fatal(err)
	}

	// A directory whose files are not ours to unlink stands in for one written
	// as root: a mode without write on it says the same thing to RemoveAll.
	stuck := filepath.Join(task.repoDir(), "vendor")
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "held.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { // else the temp directory cannot be cleaned up either
		left, _ := filepath.Glob(task.repoDir() + ".trash-*/vendor")
		for _, d := range append(left, stuck) {
			os.Chmod(d, 0o700)
		}
	}()

	if err := task.dropWorkspace(); err != nil {
		t.Fatal(err)
	}
	if exists(task.repoDir()) {
		t.Fatal("the workspace path is still taken")
	}
	if err := task.freshClone(); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(task.repoDir(), ".git")) {
		t.Error("picking the task up again did not clone")
	}
}
