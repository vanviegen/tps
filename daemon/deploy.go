package daemon

import (
	"errors"
	"fmt"
	"strings"
)

// Deploying: a project whose Containerfile.dev declares a deploy command
// (LABEL tps.deploy="...") has it run on a commit of its default branch, in
// the project's deploy task (see jobs.go): the tip, or the one a task was
// merged as — which deploys that task and every one merged before it, and
// takes back those merged after it where they were deployed. It runs when the
// user asks, and after every merge where the project says so (AutoDeploy). A
// deploy needs what the host has and the image does not: the host's files the
// project's settings name for the deploy are mounted into that task's
// container alone (see Task.mountsL). Where the last deploy is
// on the branch, and so which tasks it holds, is the board's to work out (see
// branch.go).

// The deploy task's service that runs the deploy.
const deployService = "deploy"

// Deploy deploys a commit of the branch, or its tip for none: now, or after
// the deploy that runs already. Only what is on the branch now is deployed —
// not what a rewrite of it left behind.
func (p *Project) Deploy(commit string) error {
	if commit != "" && !gitOK(p.dir(), "merge-base", "--is-ancestor", commit, p.defaultBranch) {
		return errors.New("That commit is not on " + p.defaultBranch + " (any more)")
	}
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	if p.deploy == "" {
		return errors.New("Containerfile.dev on " + p.defaultBranch + " declares no deploy (LABEL tps.deploy=\"command\")")
	}
	p.deployNext, p.deployWanted = commit, true
	if !p.deploying {
		p.deploying = true
		go p.runDeploy()
	}
	return nil
}

// deployAfterMerge deploys what was just merged, where the project says so.
func (p *Project) deployAfterMerge() {
	p.m.mu.Lock()
	auto := p.info.AutoDeploy
	p.m.mu.Unlock()
	if auto {
		_ = p.Deploy("")
	}
}

// runDeploy deploys what is wanted, until nothing is: a deploy asked for
// while one runs comes after it — unless that one failed, which is for the
// user to look at first.
func (p *Project) runDeploy() {
	jt := p.jobTask(deployTid)
	for {
		p.m.mu.Lock()
		commit, wanted := p.deployNext, p.deployWanted
		p.deployWanted = false
		if !wanted {
			p.deploying = false
			p.m.mu.Unlock()
			return
		}
		p.m.mu.Unlock()
		jt.setJobPhase(PhaseMerging)
		if !p.deployOnce(jt, commit) {
			p.m.mu.Lock()
			p.deploying, p.deployWanted = false, false
			p.m.mu.Unlock()
			return
		}
	}
}

// deployOnce deploys a commit of the branch (its tip, for none), and says
// whether that went through.
func (p *Project) deployOnce(jt *Task, commit string) bool {
	fail := func(what string, detail ...string) bool {
		jt.note("Deploy failed: "+what, detail...)
		jt.setJobPhase(PhaseHuman)
		p.carryOn("", fmt.Sprintf("the work is merged, but the deploy failed (%s), so it may not be live:\n\n%s\n\n"+
			"No deploy follows until the user starts one. Find out why it failed; where the fix is not the task's to make "+
			"(credentials, say), say so, and leave it to the user.", what, indent(strings.Join(detail, "\n"))))
		return false
	}
	sha, err := jt.reset(commit)
	if err != nil {
		return fail(err.Error())
	}
	// The workspace's own Containerfile.dev, which may be newer than what the
	// branch said when the deploy was asked for.
	if containerfileDeclarations(jt.containerfile()).deploy == "" {
		return fail("Containerfile.dev declares no deploy any more")
	}
	why := jt.runJob(deployService, "deploying "+p.defaultBranch+" at "+sha[:min(len(sha), 10)])
	log := serviceLogTail(jt.servicesDir(), deployService)
	if why != "" {
		return fail(why, log)
	}
	jt.note("Deployed ✔", log)
	p.deployed(sha)
	jt.setJobPhase(PhaseMerged)
	p.carryOn(sha, "the work is merged and deployed. The end of the deploy's output:\n\n"+indent(log))
	return true
}

// carryOn sends back in the agents waiting for the deploy of their work (see
// landed): with a deploy of sha that holds it, or with a failed one (no
// sha), after which none follows by itself.
func (p *Project) carryOn(sha, outcome string) {
	p.m.mu.Lock()
	waiting := map[*Task]string{}
	for _, t := range p.tasks {
		if t.info.AwaitDeploy != "" {
			waiting[t] = t.info.AwaitDeploy
		}
	}
	p.m.mu.Unlock()
	for t, commit := range waiting {
		if sha == "" || gitOK(p.dir(), "merge-base", "--is-ancestor", commit, sha) {
			t.carryOn(outcome)
		}
	}
}

// deployedRef is where the project repo keeps the commit deployed last. A
// ref rather than a setting: task workspaces fetch it (see fetchDeployed), so
// an agent can see what is live — the version it has, above all — and it
// keeps that commit from being collected after a rewrite of the branch.
const deployedRef = "refs/tps/deployed"

// deployed records a deploy of commit sha.
func (p *Project) deployed(sha string) {
	if _, err := git(p.dir(), "update-ref", deployedRef, sha); err != nil {
		logf("%s: recording the deploy: %v", p.pid, err)
	}
	p.refreshBranch()
}

// deployedCommit is the commit deployed last, or none.
func (p *Project) deployedCommit() string {
	sha, _ := git(p.dir(), "rev-parse", "--verify", "--quiet", deployedRef)
	return sha
}

// fetchDeployed brings the project's deployedRef into a workspace, where
// there is one: most projects never deploy, so a missing one is no failure.
func fetchDeployed(repo string) {
	_, _ = git(repo, "fetch", "--quiet", hostRemote, "+"+deployedRef+":"+deployedRef)
}

// fetchHost brings a workspace what the host has: the default branch, and the
// deployed commit.
func fetchHost(repo string) error {
	if _, err := git(repo, "fetch", "--quiet", hostRemote); err != nil {
		return err
	}
	fetchDeployed(repo)
	return nil
}
