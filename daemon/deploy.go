package daemon

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Deploying: a project whose Containerfile.dev declares a deploy command
// (LABEL tps.deploy="...") has it run on a commit of its default branch, in
// the project's deploy task (see jobs.go): the tip, or the one a task was
// merged as — which deploys that task and every one merged before it, and
// takes back those merged after it where they were deployed. It runs when the
// user asks, and after every merge where the project says so (AutoDeploy). A
// deploy needs what the host has and the image does not: the credentials the
// project's settings name (DeployMounts) are mounted into that task's
// container alone, read-only. One that needs a person — a one-time password,
// say — asks with ask-human (see ask.go). Where the last deploy is on the
// branch, and so which tasks it holds, is the board's to work out (see
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
		jt.setJobPhase(PhaseMerge)
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
		jt.note("deploy failed: "+what, detail...)
		jt.setJobPhase(PhaseHuman)
		return false
	}
	sha, err := jt.reset(commit)
	if err != nil {
		return fail(err.Error())
	}
	// The workspace's own Containerfile.dev, which may be newer than what the
	// branch said when the deploy was asked for.
	if _, _, deploy := containerfileDeclarations(jt.containerfile()); deploy == "" {
		return fail("Containerfile.dev declares no deploy any more")
	}
	jt.note("deploying " + p.defaultBranch + " at " + sha[:min(len(sha), 10)])
	s, err := jt.runJob(deployService)
	log := serviceLogTail(jt.servicesDir(), deployService)
	switch {
	case err != nil:
		return fail(err.Error(), log)
	case s.Status == "stopped":
		return fail("it was stopped", log)
	case s.Code == nil:
		return fail("it left no exit code", log)
	case *s.Code != 0:
		return fail(fmt.Sprintf("exit code %d", *s.Code), log)
	}
	jt.note("deployed ✔", log)
	p.deployed(sha)
	jt.setJobPhase(PhaseDone)
	return true
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
	_, _ = git(repo, "fetch", "--quiet", "origin", "+"+deployedRef+":"+deployedRef)
}

// The home directory of uid 1000 in an image, by image: where the deploy's
// credentials go that live in the home of the host's user (see mounts).
var (
	imageHomes   = map[string]string{}
	imageHomesMu sync.Mutex
)

func imageHome(image string) (string, error) {
	imageHomesMu.Lock()
	defer imageHomesMu.Unlock()
	if dir, ok := imageHomes[image]; ok {
		return dir, nil
	}
	r, err := runCmd([]string{"podman", "run", "--rm", "--user", "1000:1000", "--entrypoint", "sh", image, "-c", "echo ~"}, RunOpts{Timeout: time.Minute})
	dir := strings.TrimSpace(r.Out)
	if err != nil {
		return "", fmt.Errorf("finding the home directory in the image: %w", err)
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("the image has no home directory for uid 1000 (%q)", dir)
	}
	imageHomes[image] = dir
	return dir, nil
}

// mounts are the deploy task's credentials, as host:container pairs: each
// path the project's settings name, where the host has it, and the same path
// in the container — a path in the host user's home being one in the home of
// the container's.
func (t *Task) mounts(image string) ([]string, error) {
	t.lock()
	paths := t.p.info.DeployMounts
	t.unlock()
	if t.tid != deployTid || len(paths) == 0 {
		return nil, nil
	}
	inside, err := imageHome(image)
	if err != nil {
		return nil, err
	}
	var mounts []string
	for _, path := range paths {
		host, target := path, path
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			host, target = filepath.Join(home(), rest), filepath.Join(inside, rest)
		}
		if !exists(host) {
			return nil, fmt.Errorf("%s, named in the project's deploy settings, does not exist", path)
		}
		mounts = append(mounts, host+":"+target)
	}
	return mounts, nil
}
