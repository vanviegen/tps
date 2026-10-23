package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The test record: what the last run of a task's test service passed on, so
// that neither the review nor the merge runs it again for the same work. A run
// counts for the tree it ended on, if the work did not change while it ran —
// save while it waited on a human (see askHuman), whose answer may well be
// files: accepted screenshots, say. The guest tool writes down the trees as it
// goes, each line of the service's trees file an event and the tree of /work
// at that moment: "start" as the service is launched, and "ask" and "answer"
// around every question put to a human under it. The end is read here.

// TestRun is the record of a task's last run of the test service.
type TestRun struct {
	At     int64  `json:"at"`               // ms epoch the run ended at, which tells one run from the next
	Tree   string `json:"tree,omitempty"`   // the tree it ended on, once read
	Passed bool   `json:"passed,omitempty"` // it exited 0, and the tree is the one it ran on
}

const treesFile = "trees"

// noteTree writes down the tree of the work as it is now, for the service in
// dir: the guest tool's side of the record. Run from outside a repository,
// there is nothing to write down, and the run counts for nothing.
func noteTree(dir, event string) {
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return
	}
	tree, err := writeTree(top)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, treesFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", event, tree)
}

// sameWork: two trees of the work hold the same, the commit message aside —
// which is no part of what is merged (see commitMessageFile).
func sameWork(repo, a, b string) bool {
	return a == b || gitOK(repo, "diff", "--quiet", a, b, "--", ".", ":(exclude)"+commitMessageFile)
}

// testVerdict reads a finished run of the service in dir: the tree of repo it
// ended on, whether it passed on that, whether a human changed the work while
// it waited on them, and if it did not pass, why.
func testVerdict(dir, repo string) (tree string, passed, accepted bool, why string) {
	tree, err := writeTree(repo)
	if err != nil {
		return "", false, false, err.Error()
	}
	code, err := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(dir, "exit"))))
	if err != nil {
		return tree, false, false, "it did not finish"
	}
	if code != 0 {
		return tree, false, false, fmt.Sprintf("exit code %d", code)
	}
	var events [][]string
	for _, line := range strings.Split(strings.TrimSpace(readFile(filepath.Join(dir, treesFile))), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			events = append(events, f)
		}
	}
	if len(events) == 0 || events[0][0] != "start" {
		return tree, false, false, "there is no record of the work it started on"
	}
	// Between the start and the first question, from each answer to the next
	// question, and from the last answer to the end, the work must stand still.
	last := events[0][1]
	for i := 1; i < len(events); i++ {
		switch ev := events[i]; {
		case ev[0] == "ask" && i+1 < len(events) && events[i+1][0] == "answer":
			if !sameWork(repo, last, ev[1]) {
				return tree, false, false, "the work changed while it ran"
			}
			accepted = accepted || !sameWork(repo, ev[1], events[i+1][1])
			last = events[i+1][1]
			i++
		default:
			return tree, false, false, "a question it asked went unanswered"
		}
	}
	if !sameWork(repo, last, tree) {
		return tree, false, false, "the work changed while it ran"
	}
	return tree, true, accepted, ""
}

// recordTest reads the run of the test service that ended at `at` into the
// task's record, and says how it went in the log.
func (t *Task) recordTest(at int64) {
	dir := filepath.Join(t.servicesDir(), testService)
	tree, passed, _, why := testVerdict(dir, t.repoDir())
	t.lock()
	r := t.info.Tested
	if r == nil || r.At != at {
		t.unlock()
		return // a later run has the record already
	}
	r.Tree, r.Passed = tree, passed
	t.p.m.saveL()
	t.unlock()
	log := serviceLogTail(t.servicesDir(), testService)
	if passed {
		t.note("test passed ✔ on the work as it stands", log)
	} else {
		t.note("test did not pass: "+why, log)
	}
}

// tested: the test service passed on this tree, as far as the task's record
// goes (see recordTest).
func (t *Task) tested(tree string) bool {
	t.lock()
	r := t.info.Tested
	t.unlock()
	return r != nil && r.Passed && r.Tree != "" && sameWork(t.repoDir(), r.Tree, tree)
}
