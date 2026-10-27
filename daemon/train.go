package daemon

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The merge train: what puts merged tasks on the default branch, one batch at
// a time. A task being merged becomes a commit on the branch as its workspace
// last saw it (see merge) and waits in the merge phase, its commit named in
// Landing, until the train takes it: every task waiting when a round starts
// is in it, so tasks merged while a check runs are checked together in the next
// one. A round puts the batch's commits on the tip of the branch in the train's
// own workspace, runs the project's check on the result (see checkLabel), and
// moves the branch there if it passes — skipping the check where the result is
// a tree a task's own run passed on already (see tested.go), and where the
// project has none at all. A batch that fails is taken apart, its tasks
// checked one at a time from then on; a task that fails alone gets its agent
// sent in, its workspace already holding what was checked. Questions the check puts to a
// human are the train task's (see ask.go), and what the human changed while
// answering — accepted screenshots, say — lands as a commit of its own.

// startTrain sees to it that the train runs. One that runs already looks for
// waiting tasks again before it stops, so there is nothing to tell it.
func (p *Project) startTrain() {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	if p.training {
		return
	}
	p.training = true
	go p.runTrain()
}

func (p *Project) runTrain() {
	for {
		p.m.mu.Lock()
		batch := p.trainBatchL()
		if len(batch) == 0 {
			p.training = false
			jt := p.tasks[trainTid]
			p.m.mu.Unlock()
			if jt != nil {
				jt.setJobPhase(PhaseDone)
			}
			return
		}
		p.m.mu.Unlock()
		p.trainRound(batch)
	}
}

// trainBatchL is what the next round takes: the tasks waiting, in the order
// they were merged in — up to the first that is to be tested on its own, or
// that one alone when it is first.
func (p *Project) trainBatchL() []*Task {
	var queued []*Task
	for _, t := range p.tasks {
		if t.info.Phase == PhaseMerge && t.info.Landing != "" {
			queued = append(queued, t)
		}
	}
	sort.Slice(queued, func(i, j int) bool {
		a, b := queued[i], queued[j]
		if a.info.PhaseAt != b.info.PhaseAt {
			return a.info.PhaseAt < b.info.PhaseAt
		}
		x, _ := strconv.Atoi(a.tid)
		y, _ := strconv.Atoi(b.tid)
		return x < y
	})
	for i, t := range queued {
		if p.alone[t.tid] {
			return queued[:max(i, 1)]
		}
	}
	return queued
}

// trainRound runs one batch through; whatever it does not land stays waiting,
// or is handed back to its task.
func (p *Project) trainRound(batch []*Task) {
	jt := p.jobTask(trainTid)
	jt.setJobPhase(PhaseMerge)
	branch := p.defaultBranch
	if _, err := jt.reset(""); err != nil {
		p.trainFailed(batch, err)
		return
	}
	repo := jt.repoDir()
	// Each task's commit on top of the ones before it: as it is where it was
	// made on the tip (a fast-forward), and picked onto it otherwise.
	var riders []*Task
	var landings, commits []string
	for _, t := range batch {
		t.lock()
		sha := t.info.Landing
		t.unlock()
		if sha == "" {
			continue // taken back meanwhile
		}
		if _, err := git(repo, "fetch", "--quiet", t.repoDir(), landingRef); err != nil {
			p.trainFailed([]*Task{t}, err)
			continue
		}
		if fetched, _ := git(repo, "rev-parse", "FETCH_HEAD"); fetched != sha {
			continue // merged again meanwhile: the next round has the new one
		}
		head, _ := git(repo, "rev-parse", "HEAD")
		if parent, _ := git(repo, "rev-parse", sha+"^"); parent == head {
			_, err := git(repo, "reset", "--quiet", "--hard", sha)
			if err != nil {
				p.trainFailed(batch, err)
				return
			}
		} else if _, err := git(repo, "cherry-pick", "--keep-redundant-commits", sha); err != nil {
			_, _ = git(repo, "cherry-pick", "--abort")
			// Not onto the branch itself: the branch moved under the task, which
			// merges afresh (and has its agent resolve what conflicts). Onto the
			// others in the batch: it waits for them to land first.
			if len(riders) == 0 {
				jt.note(fmt.Sprintf("%s does not apply to the latest %s; merging it afresh", taskName(t), branch))
				_ = t.Merge("")
			}
			continue
		}
		commit, _ := git(repo, "rev-parse", "HEAD")
		riders, landings, commits = append(riders, t), append(landings, sha), append(commits, commit)
	}
	if len(riders) == 0 {
		return
	}
	names := taskNames(riders)
	switch {
	case containerfileDeclarations(jt.containerfile()).check == "":
		jt.note(fmt.Sprintf("%s: Containerfile.dev declares no check (LABEL %s); nothing to run", names, checkLabel))
	case len(riders) == 1 && commits[0] == landings[0] && riders[0].tested(landings[0]+"^{tree}"):
		jt.note(fmt.Sprintf("%s: its check passed on exactly this; not running it again", names))
	default:
		_, err := jt.runJob(checkService, fmt.Sprintf("checking %s on the latest %s", names, branch))
		passed, accepted, why := false, false, ""
		if err != nil {
			why = err.Error()
		} else {
			_, passed, accepted, why = testVerdict(filepath.Join(jt.servicesDir(), checkService), repo)
		}
		log := serviceLogTail(jt.servicesDir(), checkService)
		if !passed {
			if len(riders) > 1 {
				jt.note(fmt.Sprintf("The check of %s did not pass (%s); checking them one at a time", names, why), log)
				p.m.mu.Lock()
				for _, t := range riders {
					p.alone[t.tid] = true
				}
				p.m.mu.Unlock()
				return
			}
			jt.note(fmt.Sprintf("The check of %s did not pass (%s)", names, why), log)
			p.m.mu.Lock()
			delete(p.alone, riders[0].tid)
			p.m.mu.Unlock()
			riders[0].mergeTestFailed(why, log)
			return
		}
		jt.note(fmt.Sprintf("The check of %s passed ✔", names), log)
		if accepted {
			// Stamped as the last task's, so that it is part of that task's
			// landing on the board, and deploying up to it deploys what was accepted.
			message, err := riders[len(riders)-1].stamped("Accept test output\n\nAs accepted while testing " + names + ".")
			if err == nil {
				_, err = git(repo, "add", "-A")
			}
			if err == nil {
				_, err = git(repo, "commit", "--quiet", "--no-verify", "-m", message)
			}
			if err != nil {
				jt.noteErr("Committing what was accepted failed", err)
			}
		}
	}
	// Only the tasks the check was run for may land, as they were then.
	p.m.mu.Lock()
	for i, t := range riders {
		if t.info.Phase != PhaseMerge || t.info.Landing != landings[i] {
			p.m.mu.Unlock()
			jt.note(taskName(t) + " was taken back while it was checked; going again without it")
			return
		}
	}
	p.m.mu.Unlock()
	before, _ := git(p.dir(), "rev-parse", branch)
	if err := p.fastForward(repo); err != nil {
		// A branch that moved meanwhile is the next round's to catch up with;
		// one that did not cannot be moved here, and nothing else will move it.
		if after, _ := git(p.dir(), "rev-parse", branch); after == before {
			p.trainFailed(riders, err)
		}
		return
	}
	p.m.mu.Lock()
	for _, t := range riders {
		delete(p.alone, t.tid)
	}
	p.m.mu.Unlock()
	for _, t := range riders {
		t.landed("Merged into " + branch + " ✔")
	}
	jt.note(fmt.Sprintf("%s merged into %s ✔", names, branch))
	p.deployAfterMerge()
}

// trainFailed hands the tasks back to their humans: the train cannot take
// them, for reasons of its own rather than theirs.
func (p *Project) trainFailed(tasks []*Task, err error) {
	p.jobTask(trainTid).noteErr("The merge train failed", err)
	for _, t := range tasks {
		t.noteErr("The merge train failed; the task is yours", err)
		t.lock()
		if t.info.Phase == PhaseMerge {
			t.setPhaseL(PhaseHuman)
		}
		t.unlock()
	}
}

// mergeTestFailed sends the task's agent in to fix what failed the check of
// its merge; its workspace holds that very merge (see replant). The task
// stays in the merge phase, so the agent's turn ends in another merge.
func (t *Task) mergeTestFailed(why, log string) {
	branch := t.p.defaultBranch
	t.note("Its merge into the latest "+branch+" did not pass the check ("+why+"); sending the agent in", log)
	t.kick(mergeTestPrompt(branch, why, log))
}

// taskName is how the train's log names a task.
func taskName(t *Task) string {
	t.lock()
	defer t.unlock()
	return fmt.Sprintf("#%s %s", t.tid, strings.TrimSpace(t.info.Title))
}

func taskNames(tasks []*Task) string {
	names := make([]string, len(tasks))
	for i, t := range tasks {
		names[i] = taskName(t)
	}
	return strings.Join(names, ", ")
}
