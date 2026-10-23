package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A run of the test counts for the work it ended on if the work stood still
// while it ran — save while it waited on a human, and save the commit message.
func TestTestVerdict(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "one\n")
	t.Chdir(repo) // where the guest tool runs
	dir := t.TempDir()
	run := func(code string, steps ...func()) (bool, bool, string) {
		os.Remove(filepath.Join(dir, treesFile))
		noteTree(dir, "start")
		for _, step := range steps {
			step()
		}
		write(t, filepath.Join(dir, "exit"), code+"\n")
		_, passed, accepted, why := testVerdict(dir, repo)
		return passed, accepted, why
	}
	edit := func(name, content string) func() { return func() { write(t, filepath.Join(repo, name), content) } }
	ask := func() { noteTree(dir, "ask") }
	answer := func() { noteTree(dir, "answer") }

	if passed, accepted, why := run("0"); !passed || accepted {
		t.Errorf("a plain pass: %v %v %s", passed, accepted, why)
	}
	if passed, _, why := run("1"); passed || why != "exit code 1" {
		t.Errorf("a failure: %v %s", passed, why)
	}
	if passed, _, why := run("0", edit("a.txt", "two\n")); passed || !strings.Contains(why, "changed") {
		t.Errorf("work changed while it ran: %v %s", passed, why)
	}
	if passed, _, why := run("0", edit(commitMessageFile, "Say so\n")); !passed {
		t.Errorf("a commit message written meanwhile: %s", why)
	}
	if passed, accepted, why := run("0", ask, edit("shot.png", "accepted\n"), answer); !passed || !accepted {
		t.Errorf("a human's acceptance: %v %v %s", passed, accepted, why)
	}
	if passed, _, why := run("0", ask, answer, edit("a.txt", "three\n")); passed || !strings.Contains(why, "changed") {
		t.Errorf("work changed after the answer: %v %s", passed, why)
	}
	if passed, _, why := run("0", ask); passed || !strings.Contains(why, "unanswered") {
		t.Errorf("an unanswered question: %v %s", passed, why)
	}
}

// A question waits for its answer, from the dashboard or from the service
// that answers it, and is gone once answered.
func TestAskHuman(t *testing.T) {
	root := t.TempDir()
	st := &guestTool{root: root}
	if code := st.askHuman([]string{"Look?"}); code != 2 {
		t.Errorf("a question nothing answers: %d", code)
	}
	ask := func(args ...string) chan int {
		done := make(chan int, 1)
		go func() { done <- st.askHuman(args) }()
		return done
	}
	waitAsk := func() askRequest {
		for i := 0; i < 50; i++ {
			if asks := readAsks(root); len(asks) == 1 {
				return asks[0]
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("no question appeared")
		return askRequest{}
	}

	done := ask("Accept the screenshots?", "--option", "Accept", "--option", "Reject", "--port", "8080")
	req := waitAsk()
	if req.Question != "Accept the screenshots?" || len(req.Options) != 2 || req.Port != 8080 {
		t.Errorf("the question: %+v", req)
	}
	data, _ := json.Marshal(askAnswer{Option: 1})
	if err := writeAtomic(filepath.Join(root, askDir, req.ID+".answer"), data); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 1 {
		t.Errorf("the answer's exit code: %d", code)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, askDir)); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}

	// A service of its own answers it by exiting.
	if code := st.askHuman([]string{"Have a look", "--service", "look"}); code != 2 {
		t.Errorf("a service with no command: %d", code)
	}
	_ = os.MkdirAll(filepath.Join(root, ".declared"), 0o755)
	write(t, filepath.Join(root, ".declared", "look"), "sleep 0.5; exit 4\n")
	if code := st.askHuman([]string{"Have a look", "--service", "look"}); code != 4 {
		t.Errorf("answered by the service: %d", code)
	}
}

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
