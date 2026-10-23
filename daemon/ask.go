package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Questions for a human: 'tps-guest-tool ask-human' puts one to the user from
// inside a task's container — a script's, such as a test whose screenshots
// someone has to accept or a deploy that wants a one-time password, or the
// agent's own, to have the user look at something — and waits for the answer.
// Each question is a file in the services directory's askDir, <id>.json, and
// its answer <id>.answer beside it, written by the daemon on the dashboard's
// behalf; the tool takes both away once it has the answer. So the
// daemon learns of a question the way it learns of a service, by reading the
// directory (see syncServicesL), and a container that goes takes its questions
// with it (see resetServices). A task with a question open is flagged as one
// waiting for its human, whatever its phase.

const askDir = ".ask"

// askRequest is a question as the tool writes it down.
type askRequest struct {
	ID       string   `json:"id,omitempty"` // the file's name, filled in as it is read
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Input    bool     `json:"input,omitempty"`
	Secret   bool     `json:"secret,omitempty"`
	Service  string   `json:"service,omitempty"` // whose exit answers it, unless a button does first
	Port     int      `json:"port,omitempty"`    // of the container, for the dashboard to link to
}

// askAnswer is what the user answered: the option pressed, and what they typed.
type askAnswer struct {
	Option int    `json:"option"`
	Text   string `json:"text,omitempty"`
}

// --- the tool's side ---

func (st *guestTool) askHuman(args []string) int {
	var req askRequest
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--option", "--service", "--port":
			if i+1 == len(args) {
				return st.fail(fmt.Errorf("ask-human: %s needs a value", a))
			}
			i++
			switch a {
			case "--option":
				req.Options = append(req.Options, args[i])
			case "--service":
				req.Service = args[i]
			default:
				port, err := strconv.Atoi(args[i])
				if err != nil || port <= 0 {
					return st.fail(fmt.Errorf("ask-human: '%s' is not a port", args[i]))
				}
				req.Port = port
			}
		case "--input":
			req.Input = true
		case "--secret":
			req.Input, req.Secret = true, true
		default:
			if strings.HasPrefix(a, "--") || req.Question != "" {
				return st.fail(fmt.Errorf("ask-human: unexpected '%s'; run without arguments for usage", a))
			}
			req.Question = a
		}
	}
	if strings.TrimSpace(req.Question) == "" {
		return st.fail(errors.New("ask-human: what is the question?"))
	}
	if len(req.Options) == 0 && !req.Input && req.Service == "" {
		return st.fail(errors.New("ask-human: nothing to answer with: give an --option, --input or a --service"))
	}
	var serviceDir string
	if req.Service != "" {
		dir, err := st.dir(req.Service)
		if err != nil {
			return st.fail(err)
		}
		serviceRepair(dir)
		if !serviceAlive(dir) {
			if err := st.launch(req.Service, nil); err != nil {
				return st.fail(err)
			}
		}
		serviceDir = dir
	}
	dir := filepath.Join(st.root, askDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st.fail(err)
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	file, answerFile := filepath.Join(dir, id+".json"), filepath.Join(dir, id+".answer")
	cleanup := func() { os.Remove(file); os.Remove(answerFile) }
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigs
		cleanup()
		os.Exit(130)
	}()
	// Under the test service, the work as it stands on either side of the wait
	// is written down: what the human changes meanwhile is theirs (see tested.go).
	var under string
	if os.Getenv(serviceEnv) == testService {
		under = filepath.Join(st.root, testService)
		noteTree(under, "ask")
	}
	data, _ := json.Marshal(req)
	if err := writeAtomic(file, data); err != nil {
		return st.fail(err)
	}
	var ans askAnswer
	code, answered := -1, false
	for code < 0 {
		time.Sleep(300 * time.Millisecond)
		if data, err := os.ReadFile(answerFile); err == nil && json.Unmarshal(data, &ans) == nil {
			code, answered = max(ans.Option, 0), true
		} else if serviceDir != "" && !serviceAlive(serviceDir) {
			serviceRepair(serviceDir)
			code, _ = strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(serviceDir, "exit"))))
			fmt.Fprintf(os.Stderr, "'%s' exited with code %d\n", req.Service, code)
		}
	}
	cleanup()
	if under != "" {
		noteTree(under, "answer")
	}
	switch {
	case answered && req.Input:
		fmt.Println(ans.Text)
	case answered && ans.Option < len(req.Options):
		fmt.Println(req.Options[ans.Option])
	}
	return code
}

// writeAtomic puts a file in place whole, so that whoever reads the
// directory never finds half of it.
func writeAtomic(file string, data []byte) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// --- the daemon's side ---

// readAsks reads the questions waiting in a services dir, oldest first.
func readAsks(servicesDir string) []askRequest {
	dir := filepath.Join(servicesDir, askDir)
	entries, _ := os.ReadDir(dir)
	var asks []askRequest
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || exists(filepath.Join(dir, id+".answer")) {
			continue
		}
		var req askRequest
		if json.Unmarshal([]byte(readFile(filepath.Join(dir, e.Name()))), &req) == nil {
			req.ID = id
			asks = append(asks, req)
		}
	}
	sort.Slice(asks, func(i, j int) bool { return asks[i].ID < asks[j].ID })
	return asks
}

// syncAsksL reads the task's open questions, says in the log which are new,
// and publishes them.
func (t *Task) syncAsksL() {
	asks := readAsks(t.servicesDir())
	known := map[string]bool{}
	for _, a := range t.asks {
		known[a.ID] = true
	}
	for _, a := range asks {
		if !known[a.ID] {
			t.note("asks you: " + a.Question)
		}
	}
	t.asks = asks
	if len(asks) == 0 {
		t.pubL("asks", nil)
	} else {
		t.pubL("asks", asks)
	}
}

// AnswerAsk is the user's answer to one of the task's open questions.
func (t *Task) AnswerAsk(id string, option int, text string) error {
	t.lock()
	var req *askRequest
	for i := range t.asks {
		if t.asks[i].ID == id {
			req = &t.asks[i]
		}
	}
	if req == nil {
		t.unlock()
		return errors.New("That question is not open any more")
	}
	said := "answered"
	if option >= 0 && option < len(req.Options) {
		said += ": " + req.Options[option]
	}
	if req.Input && !req.Secret && text != "" {
		said += " — " + text
	}
	t.unlock()
	data, _ := json.Marshal(askAnswer{Option: option, Text: text})
	if err := writeAtomic(filepath.Join(t.servicesDir(), askDir, id+".answer"), data); err != nil {
		return err
	}
	t.note(said)
	t.lock()
	t.syncAsksL()
	t.unlock()
	return nil
}
