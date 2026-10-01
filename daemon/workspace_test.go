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
	// A test's directories go the way a task's do (see dropWorkspace): with
	// the workspaces held, so that no refresh still running reads one away.
	t.Cleanup(func() {
		m.mu.Lock()
		tasks := p.taskListL()
		m.mu.Unlock()
		for _, task := range tasks {
			task.cloneMu.Lock()
		}
	})
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
	transcript(t, task, "s.jsonl", "what the agent remembers, likewise")
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
	if !exists(task.agentDir(claudeCLI{})) || !exists(task.chatFile()) {
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
// the agent made against the rules — by merging the branch into it: what merges
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
	if exists(filepath.Join(repo, ".git", "CHERRY_PICK_HEAD")) || exists(filepath.Join(repo, ".git", "MERGE_MSG")) {
		t.Error("something is left in progress")
	}
	// The steps hold the work as it was before, the agent's commit folded in;
	// the merge is not one of them until it is resolved.
	if got := gitRun(t, repo, "log", "--format=%s", "-1", stepsBranch); got != "Human" {
		t.Errorf("last step: %q", got)
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
	// The step that attempt made of the tree first is the replay's merge.
	if gitRun(t, repo, "rev-parse", stepsBranch+"^2") != gitRun(t, origin, "rev-parse", "HEAD") {
		t.Error("the replay is not a merge of the branch into the steps")
	}
	writeWork(t, task, "a.txt", "one both\n")
	if err := task.Merge("the task"); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseDone {
		t.Errorf("phase %s", task.info.Phase)
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
	// What the merge was told to call the work is in the workspace, for the
	// agent resolving the conflicts to read and amend.
	if got := task.commitMessage(); got != "the task" {
		t.Errorf("commit message: %q", got)
	}
}

// The commit message lives in the workspace: a merge with nothing said commits
// under it, and the file itself stays off the branch.
func TestMergeTakesItsMessageFromTheWorkspace(t *testing.T) {
	task, origin := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, "a.txt", "one task\n")
	writeWork(t, task, commitMessageFile, "Do the thing\n\nAnd say why.\n")
	if err := task.Merge(""); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, origin, "log", "--format=%B", "-1"); got != "Do the thing\n\nAnd say why.\n" {
		t.Errorf("the commit message: %q", got)
	}
	if exists(filepath.Join(origin, commitMessageFile)) {
		t.Error("the commit message file reached the branch")
	}
}

// Closed without merging, a task keeps its work as a bundle and its data
// compressed, and nothing else; picked up again, the work is back as it was —
// its commits, and what was uncommitted — and so is the conversation.
func TestCloseKeepsABundle(t *testing.T) {
	task, origin := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	task.note("before the close")
	transcript(t, task, "s.jsonl", "what the agent remembers")
	writeWork(t, task, "a.txt", "one task\n")
	task.mark("Agent", "a save point")
	step := gitRun(t, task.repoDir(), "rev-parse", stepsBranch)
	base := gitRun(t, task.repoDir(), "rev-parse", "HEAD")
	writeWork(t, task, "new.txt", "new\n")
	if err := task.Close(); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseClosed || exists(task.repoDir()) || !exists(task.bundleFile()) {
		t.Fatalf("phase %s, workspace left: %v, bundle: %v", task.info.Phase, exists(task.repoDir()), exists(task.bundleFile()))
	}
	if !exists(task.archiveFile()) || exists(task.chatFile()) || exists(task.agentDir(claudeCLI{})) {
		t.Error("the data was not compressed")
	}
	changes, err := task.changes()
	if err != nil || len(changes) != 2 {
		t.Errorf("changes of the bundle: %v, %v", changes, err)
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
	if got := gitRun(t, repo, "rev-parse", "HEAD"); got != base {
		t.Errorf("HEAD is %s, not the base %s", got, base)
	}
	if !gitOK(repo, "merge-base", "--is-ancestor", step, stepsBranch) {
		t.Error("the save point's step is not among the steps")
	}
	if got := gitRun(t, repo, "status", "--porcelain"); got != " M a.txt\n?? new.txt" {
		t.Errorf("uncommitted: %q", got)
	}
	if exists(filepath.Join(repo, "b.txt")) {
		t.Error("the work was moved onto the latest branch")
	}
	if exists(task.bundleFile()) || exists(task.archiveFile()) || !exists(task.chatFile()) || !exists(task.agentDir(claudeCLI{})) {
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
	for name, want := range map[string]string{"a.txt": "one task\n", "new.txt": "new\n", "b.txt": "two\n"} {
		if got := readFile(filepath.Join(origin, name)); got != want {
			t.Errorf("%s on the branch: %q", name, got)
		}
	}
}

// Muting parks a task like closing does, and moving it on brings it back.
func TestMuteParks(t *testing.T) {
	task, _ := testTask(t)
	task.info.Phase = PhaseHuman
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, "a.txt", "one task\n")
	if err := task.MoveTo(PhaseMuted); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseMuted || exists(task.repoDir()) || !exists(task.bundleFile()) {
		t.Fatal("the muted task was not parked")
	}
	for _, phase := range []Phase{PhaseClosed, PhaseMuted} { // parked either way, and not picked up in between
		if err := task.MoveTo(phase); err != nil {
			t.Fatal(err)
		}
		if task.info.Phase != phase || exists(task.repoDir()) || !exists(task.bundleFile()) {
			t.Fatalf("%s: not parked", phase)
		}
	}
	if err := task.MoveTo(PhaseHuman); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseHuman || readFile(filepath.Join(task.repoDir(), "a.txt")) != "one task\n" {
		t.Errorf("phase %s, a.txt %q", task.info.Phase, readFile(filepath.Join(task.repoDir(), "a.txt")))
	}
}

// A closed task an older TPS left with its workspace in place is parked at
// startup: its work becomes the bundle, the workspace goes.
func TestParkAtStartup(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, "a.txt", "one task\n")
	task.info.Phase = PhaseClosed
	task.p.m.parkAll()
	if exists(task.repoDir()) || !exists(task.bundleFile()) || !exists(task.archiveFile()) {
		t.Error("the closed task was not parked")
	}
	task.p.m.parkAll() // nothing more to do
	if err := task.reopen(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(filepath.Join(task.repoDir(), "a.txt")); got != "one task\n" {
		t.Errorf("a.txt: %q", got)
	}
}

// A clean rebase is a step of its own: the branch merged into the steps. One
// that conflicts is the agent's to resolve, and is no step until it is.
func TestRebaseMergesTheBranchIntoTheSteps(t *testing.T) {
	task, origin := markTask(t)
	repo := task.repoDir()
	writeWork(t, task, "a.txt", "one task\n")
	commitUpstream(t, origin, "upstream", map[string]string{"b.txt": "two\n"})
	if err := task.Rebase(); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, repo, "log", "--format=%s", "-2", stepsBranch); got != "Rebase: onto the latest main\nHuman" {
		t.Errorf("steps: %q", got)
	}
	if gitRun(t, repo, "rev-parse", stepsBranch+"^2") != gitRun(t, origin, "rev-parse", "HEAD") ||
		gitRun(t, repo, "rev-parse", "HEAD") != gitRun(t, origin, "rev-parse", "HEAD") {
		t.Error("the rebase is not a merge of the branch, with HEAD on it")
	}
	if gitRun(t, repo, "diff", "--name-only", "HEAD") != "a.txt" {
		t.Error("the work is not uncommitted on the new base")
	}

	commitUpstream(t, origin, "upstream again", map[string]string{"a.txt": "one upstream\n"})
	if err := task.Rebase(); err != nil {
		t.Fatal(err)
	}
	if task.info.Phase != PhaseAgent {
		t.Errorf("phase: %s", task.info.Phase)
	}
	if got := gitRun(t, repo, "log", "--format=%s", "-1", stepsBranch); got != "Rebase: onto the latest main" {
		t.Errorf("the conflicted merge became a step: %q", got)
	}
}

// A directory that is not a repository yet is only made one when asked, and
// a repository without commits gets an empty first one, leaving the
// checkout's files alone.
func TestAddNewProject(t *testing.T) {
	m := testManager()
	m.dataDir = t.TempDir()
	dir := filepath.Join(t.TempDir(), "new")
	if _, missing, err := m.Add(dir, "", false); missing != "dir" || err != nil || exists(dir) {
		t.Fatalf("missing %q, err %v: asked first?", missing, err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, missing, _ := m.Add(dir, "", false); missing != "git" {
		t.Fatalf("missing %q, want git", missing)
	}
	if _, missing, err := m.Add(dir, "", true); err != nil || missing != "" {
		t.Fatal(missing, err)
	}
	if got := gitRun(t, dir, "log", "--format=%s"); got != "Initial commit" {
		t.Errorf("history: %q", got)
	}
	if got := gitRun(t, dir, "status", "--porcelain"); got != "?? a.txt" {
		t.Errorf("checkout after the first commit: %q", got)
	}
}

// The agent says the work is ready by leaving its commit message in the tree:
// a turn that changed the work without one hands the task to the user, and
// one that leaves it merges a task set to merge by itself.
func TestCommitMessageMeansReady(t *testing.T) {
	task, origin := testTask(t)
	task.p.info.Tasks = map[string]*TaskInfo{task.tid: task.info}
	task.info.OnReady = AnswerMerge
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, origin, "rev-parse", "HEAD")
	writeWork(t, task, "a.txt", "not yet\n")
	task.onTurnEnd(TurnEnd{Text: "Halfway.\n\nSummary: Started on it."})
	if task.info.Phase != PhaseHuman || gitRun(t, origin, "rev-parse", "HEAD") != head {
		t.Fatalf("work without a commit message went %s", task.info.Phase)
	}
	if got := gitRun(t, task.repoDir(), "log", "--format=%s", "-1", stepsBranch); got != "Agent: Started on it." {
		t.Errorf("the save point is named %q", got)
	}
	task.info.Phase = PhaseAgent
	writeWork(t, task, commitMessageFile, "Say it twice\n")
	task.onTurnEnd(TurnEnd{Text: "Summary: Finished it."})
	if task.info.Phase != PhaseDone {
		t.Fatalf("work with a commit message went %s", task.info.Phase)
	}
	if got := gitRun(t, origin, "log", "--format=%s", "-1"); got != "Say it twice" {
		t.Errorf("the merge: %q", got)
	}
}

// A scratch task turned into a project lands in a repository of its own that
// starts where the scratch one does, with its work unmerged in its workspace,
// to be merged there like any other task's.
func TestTurnIntoProject(t *testing.T) {
	m := testManager()
	m.dataDir = t.TempDir()
	if err := m.ensureScratch(); err != nil {
		t.Fatal(err)
	}
	scratch := m.projects["scratch"]
	tid, _ := scratch.CreateTask(map[string]any{"description": "look into it"})
	task := scratch.tasks[tid]
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	writeWork(t, task, "notes.md", "found it\n")
	if err := task.Merge("Notes"); err == nil {
		t.Error("a scratch task merged")
	}

	dir := filepath.Join(t.TempDir(), "app")
	p, moved, err := m.TurnIntoProject(task, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(scratch.tasks) != 0 || p.tasks[moved.tid] != moved || p.info.Name != "app" {
		t.Fatalf("the task did not move: %v, %v", scratch.tasks, p.tasks)
	}
	if gitRun(t, dir, "rev-parse", "HEAD") != gitRun(t, scratch.dir(), "rev-parse", "HEAD") {
		t.Error("the project does not start where the scratch repository does")
	}
	if readFile(filepath.Join(moved.repoDir(), "notes.md")) != "found it\n" {
		t.Error("the work did not come along")
	}
	if err := moved.Merge("Notes"); err != nil {
		t.Fatal(err)
	}
	if got := gitRun(t, dir, "log", "--format=%s", "-1"); got != "Notes" {
		t.Errorf("the project's history: %q", got)
	}
}
