package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanviegen/tps/hub"
)

func gitRun(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := git(repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// testTask sets up a project repository with one commit, and a task on it.
func testTask(t *testing.T) (*Task, string) {
	t.Helper()
	dir := t.TempDir()
	origin := filepath.Join(dir, "project")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, origin, "init", "--quiet", "-b", "main")
	gitRun(t, origin, "config", "user.name", "TPS")
	gitRun(t, origin, "config", "user.email", "tps@localhost")
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "--quiet", "-m", "add a.txt")

	data := filepath.Join(dir, "data")
	m := &Manager{projects: map[string]*Project{}, dataDir: data, configFile: filepath.Join(data, "config.json"), saveCh: make(chan []byte, 1), hub: hub.New(nil)}
	p := &Project{m: m, pid: "project", info: &ProjectInfo{Dir: origin}, tasks: map[string]*Task{}, defaultBranch: "main"}
	m.projects[p.pid] = p
	task := newTask(p, "1", &TaskInfo{Phase: PhaseAgent})
	p.tasks[task.tid] = task
	return task, origin
}

// What a merged task keeps and what it loses: the conversation stays, the
// clone goes, and picking the task up gives it the branch as it stands then.
func TestDropAndFreshClone(t *testing.T) {
	task, origin := testTask(t)
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
	if err := os.WriteFile(filepath.Join(origin, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "--quiet", "-m", "add b.txt")
	if err := task.freshClone(); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(task.repoDir(), "b.txt")) {
		t.Error("the new clone misses what landed on the branch since")
	}
	if exists(leftover) {
		t.Error("the old workspace came back with it")
	}
	if gitRun(t, task.repoDir(), "rev-parse", "HEAD") != gitRun(t, origin, "rev-parse", "HEAD") {
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
	task, _ := testTask(t)
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
	if !task.hasWorkspace() {
		t.Error("picking the task up again did not clone")
	}
}

// A directory where the clone was is no workspace: podman makes the mount
// point back when a container starts as the merge throws the workspace away,
// and git in it only reports that it is not a repository. Merging says what
// is the matter instead, and the next clone clears it out of the way.
func TestLeftoverDirectoryIsNoWorkspace(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureClone(); err != nil {
		t.Fatal(err)
	}
	// Nothing was changed: the merge has nothing to commit and drops the clone.
	if err := task.Merge("no changes at all"); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseDone || exists(task.repoDir()) {
		t.Fatalf("phase %q, workspace left: %v", task.info.Phase, exists(task.repoDir()))
	}

	if err := os.MkdirAll(filepath.Join(task.repoDir(), "leftover"), 0o755); err != nil {
		t.Fatal(err)
	}
	if task.hasWorkspace() {
		t.Error("a bare directory passes for a workspace")
	}
	err := task.Merge("again")
	if err == nil || !strings.Contains(err.Error(), "no workspace to merge") {
		t.Errorf("merging a leftover: %v", err)
	}

	// Picked back up, it gets a clone of the branch, leftover and all removed.
	if err := task.freshClone(); err != nil {
		t.Fatal(err)
	}
	if !task.hasWorkspace() || exists(filepath.Join(task.repoDir(), "leftover")) {
		t.Error("the fresh clone did not replace the leftover")
	}
	if err := task.Merge("nothing again"); err != nil {
		t.Fatalf("merging the fresh clone: %v", err)
	}
}

// A leftover in the way of a plain clone (one that is not a fresh one after a
// merge) is replaced too, rather than taken for the workspace it is not.
func TestCloneReplacesLeftover(t *testing.T) {
	task, _ := testTask(t)
	if err := os.MkdirAll(task.repoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task.repoDir(), "stray.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := task.ensureClone(); err != nil {
		t.Fatal(err)
	}
	if !task.hasWorkspace() {
		t.Error("the clone was skipped over a directory that is not one")
	}
	if exists(filepath.Join(task.repoDir(), "stray.txt")) {
		t.Error("the leftover survived the clone")
	}
	if !exists(filepath.Join(task.repoDir(), "a.txt")) {
		t.Error("the clone misses the branch's files")
	}
}
