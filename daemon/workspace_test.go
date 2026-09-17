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
func TestDropAndPickUp(t *testing.T) {
	task, origin := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
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
	if err := task.ensureWorkspace(); err != nil {
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
	if err := task.ensureWorkspace(); err != nil {
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
	if err := task.ensureWorkspace(); err != nil {
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
	if err := task.ensureWorkspace(); err != nil {
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
	task.info.Phase = PhaseHuman
	if err := task.ensureWorkspace(); err != nil {
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
	if err := task.ensureWorkspace(); err != nil {
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

// The dashboard's identity is what a workspace commits as: the ones made after
// it arrived, and the ones that were there before it did.
func TestIdentityReachesWorkspaces(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, task.repoDir(), "config", "user.email"); got != tpsIdentity.Email {
		t.Errorf("a workspace made without a dashboard commits as %q", got)
	}
	if err := task.p.m.setIdentity(Identity{Name: "Ada", Email: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, task.repoDir(), "config", "user.email"); got != "ada@example.com" {
		t.Errorf("the workspace in flight still commits as %q", got)
	}
	if err := task.dropWorkspace(); err != nil {
		t.Fatal(err)
	}
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, task.repoDir(), "config", "user.name"); got != "Ada" {
		t.Errorf("a fresh clone commits as %q", got)
	}
}

// commitUpstream lands a change on the project's branch behind the task's back.
func commitUpstream(t *testing.T, origin, message string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(origin, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "--quiet", "-m", message)
}

func writeWork(t *testing.T, task *Task, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(task.repoDir(), name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A rebase replays the task's work — an edit, an untracked file, and a commit
// the agent made against the rules — onto the branch as a patch: what merges
// cleanly is back as uncommitted work, what does not is left with markers and
// named, and no git operation stays in progress.
func TestReplantLeavesConflictsInTheFiles(t *testing.T) {
	task, origin := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	repo := task.repoDir()
	writeWork(t, task, "a.txt", "one task\n")
	writeWork(t, task, "new.txt", "new\n")
	writeWork(t, task, "c.txt", "committed by the agent\n")
	gitRun(t, repo, "add", "c.txt")
	gitRun(t, repo, "commit", "--quiet", "-m", "against the rules")
	commitUpstream(t, origin, "upstream", map[string]string{"a.txt": "one upstream\n", "b.txt": "two\n"})

	conflicts, err := task.replant(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0] != "a.txt" {
		t.Fatalf("conflicts: %v", conflicts)
	}
	if !strings.Contains(readFile(filepath.Join(repo, "a.txt")), "<<<<<<< HEAD") {
		t.Error("a.txt has no conflict markers")
	}
	for name, want := range map[string]string{"new.txt": "new\n", "c.txt": "committed by the agent\n", "b.txt": "two\n"} {
		if got := readFile(filepath.Join(repo, name)); got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
	if gitRun(t, repo, "rev-parse", "HEAD") != gitRun(t, origin, "rev-parse", "HEAD") {
		t.Error("the workspace is not at the branch tip")
	}
	if out := gitRun(t, repo, "diff", "--cached", "--name-only"); out != "" {
		t.Errorf("staged: %q", out)
	}
	if exists(filepath.Join(repo, ".git", "CHERRY_PICK_HEAD")) || exists(filepath.Join(repo, ".git", "MERGE_MSG")) || exists(task.patchFile()) {
		t.Error("something is left in progress")
	}
	if len(task.info.Conflicts) != 1 || len(task.info.Pending) != 1 || task.info.Pending[0].Key != "conflicts" {
		t.Errorf("conflicts %v, pending %v", task.info.Conflicts, task.info.Pending)
	}

	// Merging with the markers still in is refused; resolved, the merge lands
	// everything as one commit on the branch, and the notes about the
	// workspace go with it.
	if err := task.Merge("the task"); err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("merging with markers: %v", err)
	}
	if task.info.Phase != PhaseHuman {
		t.Errorf("phase after the refused merge: %s", task.info.Phase)
	}
	writeWork(t, task, "a.txt", "one both\n")
	if err := task.Merge("the task"); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseDone || len(task.info.Conflicts) != 0 {
		t.Errorf("phase %s, conflicts %v", task.info.Phase, task.info.Conflicts)
	}
	if got := gitRun(t, origin, "log", "--format=%s", "-3"); got != "the task\nupstream\nadd a.txt" {
		t.Errorf("history: %q", got)
	}
	if got := readFile(filepath.Join(origin, "a.txt")); got != "one both\n" {
		t.Errorf("a.txt on the branch: %q", got)
	}
	if !exists(filepath.Join(origin, "new.txt")) || !exists(filepath.Join(origin, "c.txt")) {
		t.Error("the branch misses part of the work")
	}
	if len(task.info.Pending) != 1 || task.info.Pending[0].Key != "merged" {
		t.Errorf("pending after the merge: %v", task.info.Pending)
	}
}

// A conflict a merge hits is handed to an agent: the task stays in the merge
// phase, and the workspace holds the markers for it.
func TestMergeHandsConflictsToTheAgent(t *testing.T) {
	task, origin := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, "a.txt", "one task\n")
	commitUpstream(t, origin, "upstream", map[string]string{"a.txt": "one upstream\n"})
	if err := task.Merge("the task"); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseMerge {
		t.Errorf("phase: %s", task.info.Phase)
	}
	if !strings.Contains(readFile(filepath.Join(task.repoDir(), "a.txt")), ">>>>>>> ") {
		t.Error("the workspace has no conflict to resolve")
	}
	if task.info.CommitMessage != "the task" {
		t.Errorf("commit message: %q", task.info.CommitMessage)
	}
}

// Closed without merging, a task keeps its work as a patch and its data
// compressed, and nothing else; picked up again, the work goes onto the
// branch as it is then, and the conversation is back as it was.
func TestCloseKeepsAPatch(t *testing.T) {
	task, origin := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	task.note("before the close")
	writeWork(t, task, "a.txt", "one task\n")
	writeWork(t, task, "new.txt", "new\n")
	if err := task.Close(); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseClosed || exists(task.repoDir()) {
		t.Fatalf("phase %s, workspace left: %v", task.info.Phase, exists(task.repoDir()))
	}
	if !strings.HasPrefix(readFile(task.patchFile()), "base "+gitRun(t, origin, "rev-parse", "HEAD")+"\n") {
		t.Error("the patch does not name its base")
	}
	if !exists(task.archiveFile()) || exists(task.chatFile()) || exists(task.claudeDir()) {
		t.Error("the data was not compressed")
	}
	changes, err := task.changes()
	if err != nil || len(changes) != 2 {
		t.Errorf("changes of the patch: %v, %v", changes, err)
	}
	task.note("while closed") // beside the archive; kept
	if entries := task.readChat(); len(entries) < 2 {
		t.Errorf("the chat log was not read from the archive: %d entries", len(entries))
	}

	commitUpstream(t, origin, "upstream", map[string]string{"b.txt": "two\n"})
	if err := task.reopen(); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseHuman {
		t.Errorf("phase: %s", task.info.Phase)
	}
	repo := task.repoDir()
	for name, want := range map[string]string{"a.txt": "one task\n", "new.txt": "new\n", "b.txt": "two\n"} {
		if got := readFile(filepath.Join(repo, name)); got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
	if exists(task.patchFile()) || exists(task.archiveFile()) || !exists(task.chatFile()) || !exists(task.claudeDir()) {
		t.Error("the task's data did not come back out of the archive")
	}
	log := readFile(task.chatFile())
	if !strings.Contains(log, "before the close") || !strings.Contains(log, "while closed") || strings.Index(log, "before the close") > strings.Index(log, "while closed") {
		t.Error("the chat log lost or reordered what was said around the close")
	}
	if len(task.info.Pending) == 0 || task.info.Pending[len(task.info.Pending)-1].Key != "reopened" {
		t.Errorf("pending: %v", task.info.Pending)
	}
	if err := task.Merge("closed, then merged"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(filepath.Join(origin, "a.txt")); got != "one task\n" {
		t.Errorf("a.txt on the branch: %q", got)
	}
}

// A closed task an older TPS left with its workspace in place is parked at
// startup: its work becomes the patch, the workspace goes.
func TestParkAtStartup(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, "a.txt", "one task\n")
	task.info.Phase = PhaseClosed
	task.p.m.parkFinished()
	if exists(task.repoDir()) || !exists(task.patchFile()) || !exists(task.archiveFile()) {
		t.Error("the closed task was not parked")
	}
	task.p.m.parkFinished() // nothing more to do
	if err := task.reopen(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(filepath.Join(task.repoDir(), "a.txt")); got != "one task\n" {
		t.Errorf("a.txt: %q", got)
	}
}
