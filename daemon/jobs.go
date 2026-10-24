package daemon

import (
	"cmp"
	"errors"
	"time"
)

// Jobs: two tasks of a project are TPS's own rather than the user's — the
// merge train (see train.go) and the deploy (see deploy.go). Each is a task
// like any other where it can be: a clone of the project to work in, a
// container from its Containerfile.dev, services that are the job's steps
// (the test, the deploy) with their consoles, questions for a human (see
// ask.go), and a log that tells what happened. No agent works in one and
// nobody else's work does either: its workspace is reset to the tip of the
// branch whenever it runs, so the dashboard leaves out what tasks of the
// user's are for, and so do the commands (see Cmds). It is made the first
// time it is needed, sits in Done while it has nothing to do, in Merge while
// it runs, and with the human when it failed. A train the daemon went down
// during runs again; a deploy is the user's to ask for again (see init).

const (
	trainTid  = "train"
	deployTid = "deploy"
)

var jobTitles = map[string]string{trainTid: "Merge train", deployTid: "Deploy"}

func (t *Task) job() bool { return t.tid == trainTid || t.tid == deployTid }

// jobTask is the project's job task by that tid, made if it is not there yet.
func (p *Project) jobTask(tid string) *Task {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	if t := p.tasks[tid]; t != nil {
		return t
	}
	info := &TaskInfo{Title: jobTitles[tid], Model: p.info.Defaults.Model, Phase: PhaseDone, PhaseAt: time.Now().UnixMilli()}
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
	services, caches, deploy := containerfileDeclarations(cf)
	if t.tid == deployTid && deploy != "" {
		services = append(services, declaredService{deployService, deploy})
	}
	return services, caches
}

// reset puts the job's workspace at a commit of the default branch — its tip,
// for none — and answers that commit.
func (t *Task) reset(commit string) (string, error) {
	if err := t.ensureWorkspace(); err != nil {
		return "", err
	}
	repo, branch := t.repoDir(), t.p.defaultBranch
	for _, args := range [][]string{
		{"fetch", "--quiet", "origin"},
		{"checkout", "--quiet", "--force", "-B", branch, cmp.Or(commit, "origin/"+branch)},
		{"clean", "--quiet", "-fd"},
	} {
		if _, err := git(repo, args...); err != nil {
			return "", err
		}
	}
	return git(repo, "rev-parse", "HEAD")
}

// runJob runs one of the job's services to its end — a run of it still going
// from before is stopped first — and answers how it ended. The note says what
// the run is for, and opens its console.
func (t *Task) runJob(name, note string) (serviceState, error) {
	_ = t.guestTool("stop", name) // nothing to stop, mostly
	if err := t.guestTool("start", name, "0"); err != nil {
		return serviceState{}, err
	}
	t.serviceNote(readService(t.servicesDir(), name), note)
	for {
		time.Sleep(time.Second)
		t.lock()
		up := t.status == StatusUp
		t.unlock()
		s := readService(t.servicesDir(), name)
		if s.Status == "exited" || s.Status == "stopped" {
			return s, nil
		}
		if !up || s.Status == "idle" {
			return s, errors.New("its container went away while it ran")
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
