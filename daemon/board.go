package daemon

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Other projects: '/tps/guest-tool projects', 'create-task' and 'await-task'
// let an agent see the other projects on its host, put a task in one (in Plan,
// for the user to look at and start), and wait for that task to be merged and
// deployed — a fix it needs in an upstream library, say. They are requests of
// the kind "board" (see requests.go).

// BoardRequest is one thing a task's tool asks.
type BoardRequest struct {
	Cmd         string `json:"cmd"` // projects | create | status
	Project     string `json:"project,omitempty"`
	Task        string `json:"task,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// BoardAnswer is the answer: what to print, or what went wrong; for a status,
// where the task stands.
type BoardAnswer struct {
	Text     string `json:"text,omitempty"`
	Error    string `json:"error,omitempty"`
	Phase    Phase  `json:"phase,omitempty"`
	Deploys  bool   `json:"deploys,omitempty"`  // its project deploys,
	Deployed bool   `json:"deployed,omitempty"` // and a deploy holds the task's work
}

// TaskLink is a task made on another's behalf, for the log entry that opens it.
type TaskLink struct {
	Pid     string `json:"pid"`
	Tid     string `json:"tid"`
	Title   string `json:"title"`
	Project string `json:"project"` // its name
}

// --- the tool's side ---

// askBoard puts a request to the daemon and waits for the answer.
func (st *guestTool) askBoard(req BoardRequest) (BoardAnswer, error) {
	var ans BoardAnswer
	answered, err := st.request("board", req, &ans, 30*time.Second)
	switch {
	case err != nil:
		return ans, err
	case !answered:
		return ans, errors.New("TPS did not answer")
	case ans.Error != "":
		return ans, errors.New(ans.Error)
	}
	return ans, nil
}

func (st *guestTool) projects(args []string) int {
	if len(args) > 0 {
		return st.fail(errors.New("projects: takes no arguments"))
	}
	ans, err := st.askBoard(BoardRequest{Cmd: "projects"})
	if err != nil {
		return st.fail(err)
	}
	fmt.Print(ans.Text)
	return 0
}

func (st *guestTool) createTask(args []string) int {
	if len(args) != 3 || strings.TrimSpace(args[1]) == "" || strings.TrimSpace(args[2]) == "" {
		return st.fail(errors.New("create-task: give a project, a title and a description"))
	}
	ans, err := st.askBoard(BoardRequest{Cmd: "create", Project: args[0], Title: args[1], Description: args[2]})
	if err != nil {
		return st.fail(err)
	}
	fmt.Println(ans.Text)
	return 0
}

// boardPoll is how often await-task asks again.
var boardPoll = 3 * time.Second

// awaitTask polls for where the task stands until it is in the state waited
// for: a phase, merged (done), deployed, or, with none named, any other than
// it was in at first. One that will never get there, closed without merging,
// deleted or not deployed by its project, ends the wait with 1.
func (st *guestTool) awaitTask(args []string) int {
	if len(args) == 0 || len(args) > 3 {
		return st.fail(errors.New("await-task: which task, and what to wait for?"))
	}
	task, until, seconds := args[0], "", 60
	for _, a := range args[1:] {
		if n, err := strconv.Atoi(a); err == nil && n >= 0 {
			seconds = n
		} else if a == "merged" || a == "deployed" || slices.Contains(phases, Phase(a)) {
			until = a
		} else {
			return st.fail(fmt.Errorf("await-task: '%s' is neither a state to wait for nor a number of seconds", a))
		}
	}
	if until == "merged" {
		until = string(PhaseDone)
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	var first Phase
	for {
		ans, err := st.askBoard(BoardRequest{Cmd: "status", Task: task})
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", guestToolPath, err)
			return 1
		}
		if first == "" {
			first = ans.Phase
		}
		state := string(ans.Phase)
		if ans.Deployed {
			state += ", deployed"
		}
		switch {
		case until == "deployed" && ans.Phase == PhaseDone && !ans.Deploys:
			fmt.Printf("%s: %s; its project does not deploy\n", ans.Text, state)
			return 1
		case until == "" && ans.Phase != first, until == "deployed" && ans.Deployed, until == string(ans.Phase):
			fmt.Printf("%s: %s\n", ans.Text, state)
			return 0
		case until != "" && ans.Phase == PhaseClosed:
			fmt.Printf("%s: closed without merging\n", ans.Text)
			return 1
		case !time.Now().Before(deadline):
			fmt.Printf("%s: %s, still. '%s await-task %s %s <seconds>' waits some more.\n", ans.Text, state, guestToolPath, task, until)
			return 124
		}
		time.Sleep(min(boardPoll, time.Until(deadline)))
	}
}

// --- the daemon's side ---

// checkBoardRequestsL takes up the requests the task's tool left, and answers
// each without the lock; boardBusy keeps the next tick from taking one up again.
func (t *Task) checkBoardRequestsL() {
	for _, r := range readRequests(t.servicesDir(), "board", false) {
		if !t.boardBusy[r.ID] {
			if t.boardBusy == nil {
				t.boardBusy = map[string]bool{}
			}
			t.boardBusy[r.ID] = true
			go t.answerBoard(r)
		}
	}
}

func (t *Task) answerBoard(r guestRequest) {
	var req BoardRequest
	ans, err := BoardAnswer{}, json.Unmarshal(r.Body, &req)
	if err == nil {
		switch req.Cmd {
		case "projects":
			ans.Text = t.listProjects()
		case "create":
			ans.Text, err = t.createTask(req)
		case "status":
			ans, err = t.taskStatus(req.Task)
		default:
			err = fmt.Errorf("unknown request '%s'", req.Cmd)
		}
	}
	if err != nil {
		ans = BoardAnswer{Error: err.Error()}
	}
	_ = answerRequest(t.servicesDir(), r.ID, ans)
	t.lock()
	delete(t.boardBusy, r.ID)
	t.unlock()
}

func (t *Task) listProjects() string {
	t.lock()
	defer t.unlock()
	var b strings.Builder
	b.WriteString("The projects on this host, by the name the other commands take ('.' being this task's own), and their directories:\n")
	for _, p := range t.p.m.sortedProjectsL() {
		if p.info.Scratch {
			continue
		}
		fmt.Fprintf(&b, "  %-24s %s", p.info.Name, p.dir())
		if p == t.p {
			b.WriteString("  (this task's)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// findProjectL is the project a tool's argument names: '.' for the task's
// own, or one by its name (in any case) or directory.
func (t *Task) findProjectL(name string) (*Project, error) {
	if name == "." {
		return t.p, nil
	}
	for _, p := range t.p.m.sortedProjectsL() {
		if !p.info.Scratch && (strings.EqualFold(name, p.info.Name) || name == p.dir()) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no project '%s'; 'projects' lists them", name)
}

// createTask puts a plan in a project, saying who asked for it, and the task
// in the log of the one that did.
func (t *Task) createTask(req BoardRequest) (string, error) {
	t.lock()
	p, err := t.findProjectL(req.Project)
	by, ref := cmp.Or(t.info.Title, "task "+t.tid), "."
	if p != nil && p != t.p {
		by, ref = t.p.info.Name+" › "+by, p.info.Name
	}
	t.unlock()
	if err != nil {
		return "", err
	}
	// A plan's draft is its description (see setDraftL).
	tid, err := p.CreateTask(map[string]any{"title": req.Title, "draft": req.Description, "requested": by})
	if err != nil {
		return "", err
	}
	e := newEntry("task")
	e.Task = &TaskLink{Pid: p.pid, Tid: tid, Title: req.Title, Project: p.info.Name}
	t.addEntry(e)
	return fmt.Sprintf("Created %s/%s, in Plan for the user to start: %s", ref, tid, req.Title), nil
}

// taskStatus is where the task "<project>/<number>" stands: its phase, and
// whether a deploy holds its work — which, the branch being listed newest
// first, is so for a task at or below where the last deploy is.
func (t *Task) taskStatus(ref string) (BoardAnswer, error) {
	i := strings.LastIndexByte(ref, '/')
	if i < 0 {
		return BoardAnswer{}, fmt.Errorf("'%s' names no task: give it as <project>/<number>", ref)
	}
	tid := ref[i+1:]
	t.lock()
	p, err := t.findProjectL(ref[:i])
	var ans BoardAnswer
	if err == nil {
		if task := p.tasks[tid]; task == nil {
			err = fmt.Errorf("%s is no task (any more)", ref)
		} else {
			ans = BoardAnswer{Text: ref + " (" + task.info.Title + ")", Phase: task.info.Phase, Deploys: p.deploy != ""}
		}
	}
	t.unlock()
	if ans.Deploys && ans.Phase == PhaseDone {
		log, at, _ := p.branchState()
		deployed := slices.IndexFunc(log, func(c branchCommit) bool { return c.Sha == at })
		landed := slices.IndexFunc(log, func(c branchCommit) bool { return c.Tid == tid })
		ans.Deployed = deployed >= 0 && landed >= deployed
	}
	return ans, err
}
