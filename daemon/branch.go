package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
)

// The branch as the board shows it, for a project that deploys: the commits
// of its default branch, newest first, each with the task it is the landing
// of. That is found by the trailer TPS writes into every commit a task lands
// (TPS-Task: <its stamp>), which a rebase, an amend or a squash of the branch
// carries along where a commit id would not survive. The commits between the
// tasks' are the user's own, and as much a part of what a deploy ships. The
// list reaches down to the oldest commit of a task, or to the deploy if that
// is older, and no further than branchMax commits.

const (
	taskTrailer = "TPS-Task"
	branchMax   = 300
)

// A branchCommit is one commit of the branch as the board shows it.
type branchCommit struct {
	Sha     string `json:"sha"`
	Subject string `json:"subject"`
	Tid     string `json:"tid,omitempty"` // the task whose landing it is
	stamp   string
}

// stamped is a commit message with the task's trailer added, the task being
// given its stamp the first time it lands something.
func (t *Task) stamped(message string) (string, error) {
	t.lock()
	if t.info.Stamp == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		t.info.Stamp = hex.EncodeToString(b)
		t.p.m.saveL()
	}
	stamp := t.info.Stamp
	t.unlock()
	message += "\n" // a bare subject would take the trailer into its own line otherwise
	r, err := runCmd([]string{"git", "interpret-trailers", "--trailer", taskTrailer + ": " + stamp}, RunOpts{Input: &message})
	return strings.TrimSpace(r.Out), err
}

// branchLog reads the branch down from rev, first parents only: the commits
// with their stamps, a run of commits with the same stamp counting as its
// newest — a task's landing and the test output accepted along with it.
func branchLog(dir, rev string) ([]branchCommit, error) {
	out, err := git(dir, "log", "--first-parent", "-n", strconv.Itoa(branchMax), "--format=%H%x1f%s%x1f%(trailers:key="+taskTrailer+",valueonly,separator=%x2c)%x1e", rev)
	if err != nil {
		return nil, err
	}
	var log []branchCommit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) < 3 {
			continue
		}
		stamp, _, _ := strings.Cut(f[2], ",")
		if stamp != "" && len(log) > 0 && log[len(log)-1].stamp == stamp {
			continue
		}
		log = append(log, branchCommit{Sha: f[0], Subject: f[1], stamp: stamp})
	}
	return log, nil
}

// refreshBranch publishes the branch's commits for the board, where on them
// the last deploy is (see branchState), and whether the branch holds anything
// that deploy does not.
func (p *Project) refreshBranch() {
	p.m.mu.Lock()
	deploys, head, deployed := p.deploy != "", p.head, p.info.Deployed
	p.m.mu.Unlock()
	if !deploys {
		p.pub("commits", nil)
		p.pub("deploy", nil)
		return
	}
	log, at, rewritten := p.branchState()
	p.pub("commits", log)
	p.pub("deploy", map[string]any{"at": at, "rewritten": rewritten, "pending": head != deployed})
}

// branchState reads the branch for the board, and where on it the last
// deploy is: at its commit while that is on the branch; after a rewrite of
// the branch, at the newest task it held; nowhere for none.
func (p *Project) branchState() (log []branchCommit, at string, rewritten bool) {
	p.m.mu.Lock()
	branch, deployed := p.defaultBranch, p.info.Deployed
	stamps := map[string]string{}
	for tid, t := range p.tasks {
		if t.info.Stamp != "" {
			stamps[t.info.Stamp] = tid
		}
	}
	p.m.mu.Unlock()
	log, _ = branchLog(p.dir(), branch)
	if deployed != "" {
		if gitOK(p.dir(), "merge-base", "--is-ancestor", deployed, branch) {
			at = deployed
		} else {
			rewritten = true
			held := map[string]bool{}
			old, _ := branchLog(p.dir(), deployed)
			for _, c := range old {
				held[c.stamp] = c.stamp != ""
			}
			for _, c := range log {
				if held[c.stamp] {
					at = c.Sha
					break
				}
			}
		}
	}
	last := -1
	for i := range log {
		log[i].Tid = stamps[log[i].stamp]
		if log[i].Tid != "" || log[i].Sha == at {
			last = i
		}
	}
	return log[:last+1], at, rewritten
}
