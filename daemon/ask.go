package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Questions for a human: 'tps-guest-tool ask-human' puts one to the user from
// inside a task's container — a script's, such as a test whose screenshots
// someone has to accept or a deploy that wants a one-time password, or the
// agent's own, to have the user look at something — and waits for the answer.
// Each question is a request of the kind "ask" (see requests.go), answered by
// the daemon on the dashboard's behalf. A task with a question open is flagged
// as one waiting for its human, whatever its phase.

// askRequest is a question as the tool writes it down.
type askRequest struct {
	ID       string      `json:"id,omitempty"` // the request's, filled in as it is read
	Question string      `json:"question"`     // rich text
	Fields   []askField  `json:"fields,omitempty"`
	Buttons  []askButton `json:"buttons,omitempty"`
}

// askField is something to fill in: text, a secret, or a pick among Choices.
type askField struct {
	Kind    string   `json:"kind"` // "text", "secret" or "select"
	Prompt  string   `json:"prompt"`
	Choices []string `json:"choices,omitempty"`
}

// askButton opens its URL or the container's Port where it has one, and
// answers the question where it has an ID.
type askButton struct {
	Text  string `json:"text"`
	Color string `json:"color,omitempty"`
	URL   string `json:"url,omitempty"`
	Port  int    `json:"port,omitempty"`
	ID    string `json:"id,omitempty"`
}

// The colours a button comes in: the dashboard's surfaces.
var askColors = []string{"primary", "neutral", "success", "warning", "danger"}

// askAnswer is what the user answered: the button pressed, and what the
// fields hold — or that nobody is there to answer, in a scheduled run (see
// schedule.go).
type askAnswer struct {
	Button  int      `json:"button"`
	Fields  []string `json:"fields,omitempty"`
	Refused bool     `json:"refused,omitempty"`
}

// --- the tool's side ---

// parseAsk reads ask-human's arguments. --id, --color, --url and --port are
// the last --button's, --choice the last --select's.
func parseAsk(args []string) (askRequest, error) {
	var req askRequest
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			if req.Question != "" {
				return req, fmt.Errorf("unexpected '%s'; run without arguments for usage", a)
			}
			req.Question = a
			continue
		}
		if i+1 == len(args) {
			return req, fmt.Errorf("%s needs a value", a)
		}
		i++
		v := args[i]
		var button *askButton
		if n := len(req.Buttons); n > 0 {
			button = &req.Buttons[n-1]
		}
		switch a {
		case "--button":
			req.Buttons = append(req.Buttons, askButton{Text: v})
		case "--input":
			req.Fields = append(req.Fields, askField{Kind: "text", Prompt: v})
		case "--secret":
			req.Fields = append(req.Fields, askField{Kind: "secret", Prompt: v})
		case "--select":
			req.Fields = append(req.Fields, askField{Kind: "select", Prompt: v})
		case "--choice":
			n := len(req.Fields)
			if n == 0 || req.Fields[n-1].Kind != "select" {
				return req, errors.New("--choice goes after the --select it is for")
			}
			req.Fields[n-1].Choices = append(req.Fields[n-1].Choices, v)
		case "--id", "--color", "--url", "--port":
			if button == nil {
				return req, fmt.Errorf("%s goes after the --button it is for", a)
			}
			switch a {
			case "--id":
				button.ID = v
			case "--color":
				if !slices.Contains(askColors, v) {
					return req, fmt.Errorf("'%s' is not a colour: %s", v, strings.Join(askColors, ", "))
				}
				button.Color = v
			case "--url":
				button.URL = v
			default:
				port, err := strconv.Atoi(v)
				if err != nil || port <= 0 {
					return req, fmt.Errorf("'%s' is not a port", v)
				}
				button.Port = port
			}
		default:
			return req, fmt.Errorf("unexpected '%s'; run without arguments for usage", a)
		}
	}
	if strings.TrimSpace(req.Question) == "" {
		return req, errors.New("what is the question?")
	}
	answers := false
	for i := range req.Buttons {
		b := &req.Buttons[i]
		if b.URL != "" && b.Port != 0 {
			return req, fmt.Errorf("button '%s' has both a --url and a --port", b.Text)
		}
		if b.ID == "" && b.URL == "" && b.Port == 0 {
			b.ID = b.Text
		}
		answers = answers || b.ID != ""
	}
	for _, f := range req.Fields {
		if f.Kind == "select" && len(f.Choices) == 0 {
			return req, fmt.Errorf("--select '%s' has no --choice", f.Prompt)
		}
	}
	if !answers {
		return req, errors.New("nothing to answer with: give a --button that is not only a --url or --port")
	}
	return req, checkAskLinks(req)
}

// checkAskLinks refuses links the dashboard would follow to anything but a
// web page: a javascript: link runs in the dashboard itself. A link in the
// rich text is whatever follows a "](" up to the first ")", as Aberdeen reads
// it, so every such stretch is checked. Whatever runs in the container can
// write a request without the tool, so the daemon checks it again (see
// readAsks).
func checkAskLinks(req askRequest) error {
	web := func(url string) bool {
		url = strings.ToLower(url)
		return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
	}
	for _, b := range req.Buttons {
		if b.URL != "" && !web(b.URL) {
			return fmt.Errorf("button '%s': '%s' is not an http(s) URL", b.Text, b.URL)
		}
	}
	for _, link := range strings.Split(req.Question, "](")[1:] {
		link, _, _ = strings.Cut(link, ")")
		if !web(link) {
			return fmt.Errorf("the link to '%s' in the question is not an http(s) URL", link)
		}
	}
	return nil
}

func (st *guestTool) askHuman(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(guestToolUsage)
		return 0
	}
	req, err := parseAsk(args)
	if err != nil {
		return st.fail(fmt.Errorf("ask-human: %w", err))
	}
	// Under the check, the work as it stands on either side of the wait
	// is written down: what the human changes meanwhile is theirs (see tested.go).
	var under string
	if os.Getenv(serviceEnv) == checkService {
		under = filepath.Join(st.root, checkService)
		noteTree(under, "ask")
	}
	id, err := st.put("ask", req)
	if err != nil {
		return st.fail(err)
	}
	cleanup := func() { st.drop(id) }
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigs
		cleanup()
		os.Exit(130)
	}()
	var ans askAnswer
	for !st.answer(id, &ans) {
		time.Sleep(300 * time.Millisecond)
	}
	cleanup()
	if under != "" {
		noteTree(under, "answer")
	}
	if ans.Refused {
		return st.fail(errors.New("ask-human: nobody is there to answer: this is a scheduled run"))
	}
	if len(req.Fields) == 0 {
		fmt.Println(req.Buttons[ans.Button].ID)
	} else {
		data, _ := json.Marshal(map[string]any{"button": req.Buttons[ans.Button].ID, "fields": ans.Fields})
		fmt.Println(string(data))
	}
	return ans.Button
}

// --- the daemon's side ---

// readAsks reads the questions waiting in a services dir, oldest first.
func readAsks(servicesDir string) []askRequest {
	var asks []askRequest
	for _, r := range readRequests(servicesDir, "ask", false) {
		var req askRequest
		if json.Unmarshal(r.Body, &req) == nil && checkAskLinks(req) == nil {
			req.ID = r.ID
			asks = append(asks, req)
		}
	}
	return asks
}

// syncAsksL reads the task's open questions, says in the log which are new,
// and publishes them. A scheduled run's are refused instead.
func (t *Task) syncAsksL() {
	asks := readAsks(t.servicesDir())
	if t.info.Scheduled != "" {
		for _, a := range asks {
			_ = answerRequest(t.servicesDir(), a.ID, askAnswer{Refused: true})
			t.note("Refused a question, nobody being there to answer it: " + a.Question)
		}
		asks = nil
	}
	known := map[string]bool{}
	for _, a := range t.asks {
		known[a.ID] = true
	}
	for _, a := range asks {
		if !known[a.ID] {
			t.note("Asks you: " + a.Question)
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
func (t *Task) AnswerAsk(id string, button int, fields []string) error {
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
	if button < 0 || button >= len(req.Buttons) || req.Buttons[button].ID == "" || len(fields) != len(req.Fields) {
		t.unlock()
		return errors.New("That is no answer to the question")
	}
	said := "Answered: " + req.Buttons[button].Text
	for i, f := range req.Fields {
		if f.Kind != "secret" && fields[i] != "" {
			said += " — " + fields[i]
		}
	}
	t.unlock()
	if err := answerRequest(t.servicesDir(), id, askAnswer{Button: button, Fields: fields}); err != nil {
		return err
	}
	t.note(said)
	t.lock()
	t.syncAsksL()
	t.unlock()
	return nil
}
