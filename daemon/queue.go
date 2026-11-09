package daemon

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The merge queue: what puts merged tasks on the default branch, one at a
// time. A task being merged sits in the merge phase, its tid in the project's
// Queue, the first of which is the one merging and the rest waiting their
// turn, in the order the user can change (see MoveInQueue). A merge happens in
// the task's own workspace: the work is put onto the latest default branch
// (see replant), what does not merge cleanly is the task's agent's to resolve,
// the project's test (see testLabel) is run on the result unless the merge
// skips it, and a test that fails sends the agent in to fix it — up to
// maxTestRuns runs, after which the task is the user's. Once it passes, the
// work becomes one commit on the branch, with what the test changed in it (a
// screenshot baseline accepted while it ran, say). Where the project's own
// checkout has uncommitted changes that commit would overwrite, it is held
// back, and the queue with it, until they are gone (see retryHeld).

// maxTestRuns is how often a merge's test may fail before the task goes to
// the user rather than to its agent once more.
const maxTestRuns = 10

// enqueueL puts the task at the back of the queue, and starts its merge
// where it is the only one.
func (p *Project) enqueueL(t *Task) {
	if slices.Contains(p.info.Queue, t.tid) {
		return
	}
	p.info.Queue = append(p.info.Queue, t.tid)
	t.testRuns = 0
	p.pubQueueL()
	if len(p.info.Queue) > 1 {
		t.note("Queued for merge")
	}
	p.startHeadL()
}

// dequeueL takes the task out of the queue — merged, or taken back — and a
// test of its merge that runs with it.
func (p *Project) dequeueL(t *Task) {
	i := slices.Index(p.info.Queue, t.tid)
	if i < 0 {
		return
	}
	p.info.Queue = slices.Delete(p.info.Queue, i, i+1)
	t.merging = false
	t.holdL("", nil)
	t.pubL("queued", nil)
	if t.testing {
		go t.guestTool("stop", testService)
	}
	p.pubQueueL()
	if i == 0 {
		p.startHeadL()
	}
}

// pubQueueL saves and publishes the queue: its order for the board, and for
// each task in it whether it waits its turn.
func (p *Project) pubQueueL() {
	p.m.saveL()
	p.pub("queue", slices.Clone(p.info.Queue))
	for i, tid := range p.info.Queue {
		p.tasks[tid].pubL("queued", i > 0)
	}
}

// headL: the task is the one merging, rather than waiting its turn.
func (t *Task) headL() bool {
	return len(t.p.info.Queue) > 0 && t.p.info.Queue[0] == t.tid
}

// startHeadL starts the merge of the task first in the queue, unless it is
// under way already — its agent's turn ending takes it up again (see
// onTurnEnd) — or that agent is still busy with it, or waiting out a usage
// limit, from before it was put back in the queue — or it is held back by the
// checkout's changes (see retryHeld).
func (p *Project) startHeadL() {
	if len(p.info.Queue) == 0 {
		return
	}
	t := p.tasks[p.info.Queue[0]]
	if t.merging || t.workingL() || t.info.LimitUntil > 0 || t.held != "" {
		return
	}
	t.merging = true
	go t.runMerge()
}

// MoveInQueue puts the task at place `to` of the queue, 0 being the head. A
// task put before the one merging stops that merge, which waits its turn
// again, and starts merging itself.
func (p *Project) MoveInQueue(tid string, to int) error {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	i := slices.Index(p.info.Queue, tid)
	if i < 0 {
		return errors.New("The task is not queued for merge")
	}
	head := p.tasks[p.info.Queue[0]]
	p.info.Queue = slices.Insert(slices.Delete(p.info.Queue, i, i+1), min(max(to, 0), len(p.info.Queue)-1), tid)
	p.pubQueueL()
	if p.info.Queue[0] != head.tid {
		head.note("Put back in the merge queue; it merges once its turn comes again")
		if head.testing {
			go head.guestTool("stop", testService)
		}
		p.startHeadL()
	}
	return nil
}

// runMerge takes the merge of the task at the head of the queue as far as it
// goes without the agent.
func (t *Task) runMerge() {
	defer t.p.m.work()()
	handed, err := t.mergeHead()
	go t.refreshChanges()
	t.lock()
	defer t.unlock()
	if err != nil && t.info.Phase == PhaseMerging {
		t.noteErr("Merge failed", err)
		t.setPhaseL(PhaseHuman)
	}
	if !handed {
		t.merging = false
		t.p.startHeadL() // this task again, where it was put back at the head meanwhile
	}
}

// mergeHead merges the task: lands it, hands it to its agent (reported as
// handed, the agent's turn ending taking it up again), or answers why it
// cannot. A task taken out of the queue meanwhile, or put back in it, is left
// as it is.
func (t *Task) mergeHead() (handed bool, err error) {
	repo, branch := t.repoDir(), t.p.defaultBranch
	t.lock()
	held := t.held
	t.holdL("", nil)
	t.unlock()
	if held != "" {
		if moved, err := t.land(held); !moved {
			return false, err
		}
	}
	for {
		conflicts, doubled, err := t.replant(repo)
		if err != nil {
			return false, err
		}
		if len(conflicts) > 0 || len(doubled) > 0 {
			t.note(fmt.Sprintf("Merging onto the latest %s needs a look at %s; sending the agent in", branch, strings.Join(append(conflicts, doubled...), ", ")))
			t.kick(conflictPrompt(branch, conflicts, doubled))
			return true, nil
		}
		// Whatever a replay left in the files — or an agent that reported 'merge'
		// without resolving — must not land on the branch.
		if marked, err := t.markedFiles(repo); err != nil {
			return false, err
		} else if len(marked) > 0 {
			return false, fmt.Errorf("conflict markers are left in %s", strings.Join(marked, ", "))
		}
		tree, err := landingTree(repo)
		if err != nil {
			return false, err
		}
		if base, _ := git(repo, "rev-parse", "HEAD^{tree}"); base == tree {
			t.landed("Nothing to merge; task closed", "")
			return false, nil
		}
		if containerfileDeclarations(t.containerfile()).test != "" {
			t.lock()
			skip := t.info.SkipTests
			t.testing = !skip
			t.unlock()
			why, log := "", ""
			if skip {
				t.note("Merging without running the test, as asked")
			} else {
				why = t.runJob(testService, "Testing the merge onto the latest "+branch)
				log = serviceLogTail(t.servicesDir(), testService)
			}
			t.lock()
			t.testing = false
			skip = t.info.SkipTests
			t.unlock()
			switch {
			case !t.stillHead():
				return false, nil
			case why == "":
			case skip:
				t.note("The test was skipped; merging")
			case why == "it was stopped":
				return false, errors.New("its test was stopped")
			default:
				t.lock()
				t.testRuns++
				runs := t.testRuns
				t.unlock()
				if runs >= maxTestRuns {
					return false, fmt.Errorf("the test still does not pass (%s) after %d runs; the task is yours", why, runs)
				}
				t.note(fmt.Sprintf("The merge did not pass the test (%s); sending the agent in to fix it (run %d of %d)", why, runs, maxTestRuns), log)
				t.kick(mergeTestPrompt(branch, why, log, runs))
				return true, nil
			}
		}
		// The commit the task becomes, with what the test changed, made beside
		// the workspace rather than in it, so that the file that carried the
		// message, which is not the project's, stays out.
		if tree, err = landingTree(repo); err != nil {
			return false, err
		}
		t.lock()
		title := t.info.Title
		t.unlock()
		message, err := t.stamped(cmp.Or(t.commitMessage(), strings.TrimSpace(title)))
		if err != nil {
			return false, err
		}
		sha, err := git(repo, "commit-tree", tree, "-p", "HEAD", "-m", message)
		if err != nil {
			return false, err
		}
		if _, err := git(repo, "update-ref", landingRef, sha); err != nil {
			return false, err
		}
		moved, err := t.land(sha)
		if !moved {
			return false, err
		}
		t.note(branch + " moved meanwhile; merging onto its latest")
	}
}

func (t *Task) stillHead() bool {
	t.lock()
	defer t.unlock()
	return t.info.Phase == PhaseMerging && t.headL()
}

// land puts the commit the task is to become on the branch, and reports
// whether the branch moved since the work was put onto it, for the merge to
// start over on its latest. One that would overwrite uncommitted changes in
// the project's checkout is held back instead.
func (t *Task) land(sha string) (moved bool, err error) {
	repo, branch := t.repoDir(), t.p.defaultBranch
	if !t.stillHead() {
		return false, nil
	}
	if files := t.p.blockingChanges(repo, sha); len(files) > 0 {
		t.note(fmt.Sprintf("The project's checkout has uncommitted changes in %s, which the merge would overwrite; it waits until they are committed or undone", strings.Join(files, ", ")))
		t.lock()
		t.holdL(sha, files)
		t.unlock()
		return false, nil
	}
	if err := t.p.fastForward(repo, landingRef); err != nil {
		tip, _ := git(t.p.dir(), "rev-parse", branch)
		if onto, _ := git(repo, "rev-parse", sha+"^"); tip == onto {
			return false, err
		}
		return true, nil
	}
	t.landed("Merged into "+branch+" ✔", sha)
	t.p.deployAfterMerge()
	return false, nil
}

// holdL records the commit a merge holds back, and the files of the
// checkout's in its way; none for a merge that is not held.
func (t *Task) holdL(sha string, files []string) {
	t.held = sha
	if files == nil {
		t.pubL("heldBy", nil)
	} else {
		t.pubL("heldBy", files)
	}
}

// blockingChanges are the files with uncommitted changes in the project's
// checkout — when it is on the default branch, where landing moves its tree
// along — that the commit sha of the task's clone repo changes.
func (p *Project) blockingChanges(repo, sha string) []string {
	if branch, _ := git(p.dir(), "symbolic-ref", "--short", "HEAD"); branch != p.defaultBranch {
		return nil
	}
	dirty := map[string]bool{}
	for _, args := range [][]string{{"diff", "--name-only", "-z", "HEAD"}, {"ls-files", "--others", "--exclude-standard", "-z"}} {
		out, _ := git(p.dir(), args...)
		for _, f := range strings.Split(out, "\x00") {
			dirty[f] = f != ""
		}
	}
	out, _ := git(repo, "diff", "--name-only", "-z", sha+"^", sha)
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if dirty[f] {
			files = append(files, f)
		}
	}
	return files
}

// retryHeld lands the merge held back by the checkout's changes once they
// are out of its way; the sweep looks every minute (see refreshMeta).
func (p *Project) retryHeld() {
	p.m.mu.Lock()
	var t *Task
	if len(p.info.Queue) > 0 {
		t = p.tasks[p.info.Queue[0]]
	}
	if t == nil || t.held == "" {
		p.m.mu.Unlock()
		return
	}
	sha := t.held
	p.m.mu.Unlock()
	if len(p.blockingChanges(t.repoDir(), sha)) > 0 {
		return
	}
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	if t.held == sha && !t.merging {
		t.merging = true
		go t.runMerge()
	}
}

// landingRef is where a task's clone keeps the commit it is to become, for
// the project repo to fetch (see fastForward).
const landingRef = "refs/tps/landing"
