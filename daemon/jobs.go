package daemon

import (
	"cmp"
	"fmt"
	"time"
)

// Jobs: one task of a project is TPS's own rather than the user's — the
// deploy (see deploy.go). It is a task like any other where it can be: a
// clone of the project to work in, a container from its Containerfile.dev,
// services that are the job's steps (the deploy) with their consoles, and a
// log that tells what happened. No agent works in one and nobody else's work
// does either: its workspace is reset to a commit of the branch whenever it
// runs, so the dashboard leaves out what tasks of the user's are for, and so
// do the commands (see Cmds). It is made the first time it is needed, sits in
// Done while it has nothing to do, in Merge while it runs, and with the human
// when it failed. A deploy the daemon went down during is the user's to ask
// for again (see init).

const deployTid = "deploy"

var jobTitles = map[string]string{deployTid: "Deploy"}

func (t *Task) job() bool { return t.tid == deployTid }

// jobTask is the project's job task by that tid, made if it is not there yet.
func (p *Project) jobTask(tid string) *Task {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	if t := p.tasks[tid]; t != nil {
		return t
	}
	info := &TaskInfo{Title: jobTitles[tid], Model: p.info.Defaults.Model, Effort: p.info.Defaults.Effort, Phase: PhaseMerged, PhaseAt: time.Now().UnixMilli()}
	p.info.Tasks[tid] = info
	t := newTask(p, tid, info)
	p.tasks[tid] = t
	p.m.saveL()
	t.publishL()
	return t
}

// declarations is what Containerfile.dev declares for this task: its services
// and caches — and, in the deploy task alone, the deploy as a service.
func (t *Task) declarations(cf string) ([]declaredService, []string) {
	d := containerfileDeclarations(cf)
	if t.tid == deployTid && d.deploy != "" {
		d.services = append(d.services, declaredService{deployService, d.deploy})
	}
	return d.services, d.caches
}

// reset puts the job's workspace at a commit of the default branch — its tip,
// for none — and answers that commit.
func (t *Task) reset(commit string) (string, error) {
	if err := t.ensureWorkspace(); err != nil {
		return "", err
	}
	repo, branch := t.repoDir(), t.p.defaultBranch
	if err := gitSeq(repo,
		[]string{"fetch", "--quiet", hostRemote},
		[]string{"checkout", "--quiet", "--force", "-B", branch, cmp.Or(commit, t.upstream())},
		[]string{"clean", "--quiet", "-fd"}); err != nil {
		return "", err
	}
	return git(repo, "rev-parse", "HEAD")
}

// runJob runs one of the task's services to its end — a run of it still
// going from before is stopped first — and answers why it failed, "" where it
// exited 0: a job's step, or the test of a merge (see queue.go). The note
// says what the run is for, and opens its console.
func (t *Task) runJob(name, note string) (why string) {
	_ = t.guestTool("stop", name) // nothing to stop, mostly
	if err := t.guestTool("start", name, "0"); err != nil {
		return err.Error()
	}
	t.serviceNote(readService(t.servicesDir(), name), note)
	for {
		time.Sleep(time.Second)
		t.lock()
		up := t.status == StatusUp
		t.unlock()
		switch s := readService(t.servicesDir(), name); {
		case s.Status == "stopped":
			return "it was stopped"
		case s.Status == "exited" && s.Code == nil:
			return "it left no exit code"
		case s.Status == "exited" && *s.Code != 0:
			return fmt.Sprintf("exit code %d", *s.Code)
		case s.Status == "exited":
			return ""
		case !up || s.Status == "idle":
			return "its container went away while it ran"
		}
	}
}

// setJobPhase moves the job between running (Merge) and not.
func (t *Task) setJobPhase(phase Phase) {
	t.lock()
	defer t.unlock()
	if t.info.Phase != phase {
		t.setPhaseL(phase)
	}
}
