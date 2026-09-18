package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The pi provider: pi (github.com/earendil-works/pi-mono), run in a task's
// container as one long `pi --mode rpc` talking JSON on both ends (see
// agent.go). Which model that is — anything the providers pi is logged in to
// offer — is the task's model setting; the login and the settings are the
// user's own pi directory on the host, mounted in, while the sessions the
// agent writes are the task's (see Mounts).

const piVersion = "0.85.1"

// piMount is the task's own pi directory in its container, and piAuthMount the
// user's, which every task shares.
const (
	piMount     = "/pi"
	piAuthMount = "/pi-agent"
)

const piScript = `
export PATH=/tps/bin:$PATH
echo $$ >/tmp/tps-agent.pid
exec /tps/bin/pi --mode rpc --append-system-prompt "$TPS_SYSTEM" \
	${TPS_MODEL:+--model "$TPS_MODEL"} $TPS_EXTRA
`

// piStateScript asks a throwaway pi in the container what model it would run
// on, which is where its context window comes from (see Window).
const piStateScript = `
export PATH=/tps/bin:$PATH
exec /tps/bin/pi --mode rpc --no-session ${TPS_MODEL:+--model "$TPS_MODEL"}
`

type piCLI struct{}

func (piCLI) Name() string    { return "pi" }
func (piCLI) Version() string { return piVersion }

// Install unpacks the pi release beside the toolbox's binaries, the way
// code-server is: it is a binary with files of its own beside it.
func (piCLI) Install(dir string) error {
	arch, ok := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("no pi build for %s", runtime.GOARCH)
	}
	url := "https://github.com/earendil-works/pi-mono/releases/download/v" + piVersion + "/pi-linux-" + arch + ".tar.gz"
	if err := fetch(url, func(r io.Reader) error { return untar(r, filepath.Join(dir, "pi")) }); err != nil {
		return err
	}
	return os.Symlink("../pi/pi", filepath.Join(dir, "bin", "pi"))
}

// Mounts: the user's own pi directory — its login, its settings, its
// extensions — for every task to run on, and the task's own directory for the
// sessions the agent writes, which are its memory and nobody else's.
//
// The user's directory goes in as an overlay (:O): pi has to be able to write
// in it — it locks beside its credentials, caches the model catalogue — but
// what a task writes there is nothing the host should see, an agent's changes
// to the user's setup least of all, so it all lands in a layer that goes with
// the container. The one exception is the credentials file, bound over the
// overlay as itself: a login pi refreshes has to reach the host, or the token
// it replaced would be the only one anybody else has. pi writes that file in
// place, so binding the file rather than its directory holds.
func (piCLI) Mounts(dir string) []string {
	return []string{
		"-v", dir + ":" + piMount,
		"-e", "PI_CODING_AGENT_SESSION_DIR=" + piMount + "/sessions",
		"-v", piAgentDir() + ":" + piAuthMount + ":O",
		"-v", piAuthFile() + ":" + piAuthMount + "/auth.json",
		"-e", "PI_CODING_AGENT_DIR=" + piAuthMount,
	}
}

func (piCLI) Transcripts() string { return filepath.Join("sessions", "*.jsonl") }

// piAgentDir is the user's pi directory on this host, made on first use (it is
// a mount source, so it has to exist) and resolved, as podman needs the real path.
func piAgentDir() string {
	dir := filepath.Join(home(), ".pi", "agent")
	_ = os.MkdirAll(dir, 0o700)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// piAuthFile is where pi keeps its logins, made empty when there is none yet:
// a file that is bound into a container has to exist, and a missing one would
// come up as a directory.
func piAuthFile() string {
	path := filepath.Join(piAgentDir(), "auth.json")
	if !exists(path) {
		_ = os.WriteFile(path, []byte("{}\n"), 0o600)
	}
	return path
}

// piBin is the pi binary in this host's toolbox.
func piBin() string { return filepath.Join(toolboxDir(), "bin", "pi") }

// Models are every model the providers pi is logged in to offer, as pi lists
// them: `pi --list-models` prints a table, and its model column is what
// --model takes.
func (piCLI) Models() ([]string, error) {
	r, err := runCmd([]string{piBin(), "--list-models"}, RunOpts{Dir: home(), Timeout: 60 * time.Second})
	if err != nil {
		return nil, err
	}
	models := parsePiModels(r.Out)
	if models == nil {
		return nil, fmt.Errorf("no model list in `%s`", firstLine(r.Out+r.Err))
	}
	return models, nil
}

// parsePiModels reads the model column of that table:
//
//	provider    model                context  max-out  thinking  images
//	openrouter  z-ai/glm-5.3-flash   1.0M     943.7K   yes       yes
//
// The same model offered by two of pi's providers is the one entry: pi
// resolves the name to whichever it has. Everything above the header line is
// pi talking rather than listing, most of all when it has nothing to list.
func parsePiModels(out string) []string {
	var models []string
	seen := map[string]bool{}
	listing := false
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if !listing {
			listing = fields[0] == "provider" && fields[1] == "model"
			continue
		}
		if seen[fields[1]] {
			continue
		}
		seen[fields[1]] = true
		models = append(models, fields[1])
	}
	return models
}

// Title asks this host's pi to name a task, on whichever model it defaults to
// and with its tools out of the way: this is one question and one line back.
func (piCLI) Title(description string) string {
	r, err := runCmd([]string{piBin(), "-p", "--no-session", "--no-tools", titlePrompt(description)},
		RunOpts{Dir: home(), Timeout: 30 * time.Second})
	if err != nil {
		logf("asking pi for a task title failed: %v", err)
		return ""
	}
	return titleOf(r.Out)
}

// Window: pi says nothing about what its window holds before the conversation
// does, so all the task's gauge gets is where the conversation stops growing,
// which is the model's context window. A throwaway pi in the container is
// asked, rather than the catalogue on the host, because resolving the name —
// "default" above all — is pi's to do.
func (piCLI) Window(c *Container, model, _ string) (ContextWindow, bool) {
	if model == defaultModel {
		model = ""
	}
	ask := `{"type":"get_state"}` + "\n"
	r, err := runCmd([]string{"podman", "exec", "-i", "-e", "TPS_MODEL=" + model, c.Name, "bash", "-lc", piStateScript},
		RunOpts{Input: &ask, Timeout: probeTimeout, NoCheck: true})
	if err != nil {
		return ContextWindow{}, false
	}
	for _, line := range strings.Split(r.Out, "\n") {
		var ev struct {
			Command string `json:"command"`
			Data    struct {
				Model *struct {
					ContextWindow int64 `json:"contextWindow"`
				} `json:"model"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Command != "get_state" || ev.Data.Model == nil {
			continue
		}
		if ev.Data.Model.ContextWindow > 0 {
			return ContextWindow{Limit: ev.Data.Model.ContextWindow}, true
		}
	}
	return ContextWindow{}, false
}

func (piCLI) Start(opts SessionOpts) (Session, error) {
	extra := ""
	if opts.Resume {
		extra = "--continue"
	}
	if opts.Review {
		// Nobody's conversation: it is never written to the task's sessions,
		// so the one a later --continue picks back up is still the task's own.
		extra = "--no-session"
	}
	model := opts.Model
	if model == defaultModel {
		model = "" // pi's own default, which is what it is given no --model
	}
	// No budget: pi takes no spending cap, so a task's is the one TPS keeps
	// itself, between turns rather than within one (see overBudgetL).
	env := []string{"TPS_MODEL=" + model, "TPS_SYSTEM=" + opts.System, "TPS_EXTRA=" + extra}
	p, err := newAgentProc(opts, env, piScript)
	if err != nil {
		return nil, err
	}
	s := &piSession{agentProc: p, pending: map[string]*ChatEntry{}}
	p.run(s.onLine)
	// What the session has cost so far, which a resumed one starts part-way
	// into: every turn here is accounted against that (see finish).
	s.write(map[string]any{"type": "get_session_stats"})
	return s, nil
}

type piSession struct {
	*agentProc
	costReported float64               // what the session had cost as of the last turn
	baselined    bool                  // and whether that has been read at all yet
	pending      map[string]*ChatEntry // tool calls awaiting their result
	started      time.Time             // when the turn under way was sent in
	summary      int64                 // tokens the turn under way summarised the conversation down to (see TurnEnd)
	lastText     string                // the latest message of the turn under way, which is the reviewer's answer (see review.go)
	done         *Done                 // the verdict of the turn under way, from its latest message
	badDone      string                // why that message's TPS-DONE line was unusable
	failed       bool                  // a message of the turn ended in an error pi could not retry away
	idle         bool                  // the turn is a compaction, in which nothing was asked of the agent
}

// Send: a prompt, or steering for the turn under way — pi takes the one
// command for both, and delivers a message that arrives mid-turn between the
// tool calls and the next request. A /compact is not a message at all but the
// command pi has for it (see isCompact).
func (s *piSession) Send(text string) {
	s.turnActive.Store(true)
	s.started = time.Now()
	s.lastText, s.done, s.badDone, s.failed = "", nil, "", false
	if s.idle = isCompact(text); s.idle {
		s.write(map[string]any{"type": "compact", "customInstructions": strings.TrimSpace(strings.TrimPrefix(text, "/compact"))})
		return
	}
	s.write(map[string]any{"type": "prompt", "message": text, "streamingBehavior": "steer"})
}

func (s *piSession) Stop() { s.stop(map[string]any{"type": "abort"}) }

// --- rpc events → chat entries ---

type piEvent struct {
	Type    string     `json:"type"`
	Message *piMessage `json:"message"` // message_end
	// tool_execution_end
	ToolCallID string `json:"toolCallId"`
	IsError    bool   `json:"isError"`
	// compaction_end, and the tool result: two shapes of the one field, read
	// where each belongs.
	Result       json.RawMessage `json:"result"`
	Reason       string          `json:"reason"` // manual: someone said /compact · threshold, overflow: the window filled up
	ErrorMessage string          `json:"errorMessage"`
	// response, to a command we sent
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

type piMessage struct {
	Role         string    `json:"role"`
	Content      []piBlock `json:"content"`
	StopReason   string    `json:"stopReason"` // stop | length | toolUse | error | aborted
	ErrorMessage string    `json:"errorMessage"`
}

type piBlock struct {
	Type      string         `json:"type"` // text | thinking | toolCall
	Text      string         `json:"text"`
	Thinking  string         `json:"thinking"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *piSession) onLine(line []byte) {
	var ev piEvent
	if json.Unmarshal(line, &ev) == nil {
		s.onEvent(&ev)
	}
}

func (s *piSession) onEvent(ev *piEvent) {
	switch ev.Type {
	case "agent_start":
		s.turnActive.Store(true)
		s.opts.OnTurnStart()
	// The assistant's message, whole: pi streams it as it comes in and says it
	// again here, which is soon enough for a log that shows it a line at a time.
	case "message_end":
		if ev.Message == nil || ev.Message.Role != "assistant" {
			return
		}
		s.onMessage(ev.Message)
	case "tool_execution_end":
		var result struct {
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(ev.Result, &result)
		text := resultText(result.Content)
		if text == "" {
			text = "(no output)"
		}
		if e := s.pending[ev.ToolCallID]; e != nil {
			delete(s.pending, ev.ToolCallID)
			e.Res, e.ResDetail, e.Error = oneLine(text, 140), clip(text), ev.IsError
			s.opts.OnUpdate(e)
		} else { // a result without a tracked call (shouldn't normally happen)
			e := s.entry("tool")
			e.Name, e.Res, e.ResDetail, e.Error = "result", oneLine(text, 140), clip(text), ev.IsError
			s.opts.OnEntry(e)
		}
	// Compaction: pi replacing everything it remembers with a summary of it,
	// either because the context window filled up or because somebody said
	// /compact. It only ever appends to its transcript, so the save points made
	// before this one still measure the whole conversation and reverting to one
	// undoes the compaction (see mark).
	case "compaction_end":
		e := s.entry("note")
		var result *struct {
			Before int64 `json:"tokensBefore"`
			After  int64 `json:"estimatedTokensAfter"`
		}
		if json.Unmarshal(ev.Result, &result) == nil && result != nil {
			how := "the agent's context filled up, so it was compacted"
			if ev.Reason == "manual" {
				how = "the agent compacted its context on request"
			}
			s.summary = result.After
			e.Text = fmt.Sprintf("%s: %s → %s", how, tokens(result.Before), tokens(result.After))
			e.Detail = compactNote
		} else {
			e.Text, e.Error = "compacting the agent's context failed", true
			if why := strings.TrimSpace(ev.ErrorMessage); why != "" {
				e.Text += ": " + oneLine(why, 200)
			}
		}
		s.opts.OnEntry(e)
	// The agent is done: what it cost and how much of the window it now fills
	// are pi's to tell, so the turn ends on the answer to that (see finish).
	case "agent_settled":
		s.write(map[string]any{"type": "get_session_stats"})
	case "response":
		switch ev.Command {
		case "compact":
			// A compaction that could not run says so here rather than in an
			// event of its own, and is the whole of that turn.
			if !ev.Success {
				e := s.entry("note")
				e.Text, e.Error = "compacting the agent's context failed: "+oneLine(ev.Error, 200), true
				s.opts.OnEntry(e)
			}
			s.write(map[string]any{"type": "get_session_stats"})
		case "get_session_stats":
			var stats piStats
			_ = json.Unmarshal(ev.Data, &stats)
			if !s.baselined { // the answer to the one asked for at startup
				s.baselined, s.costReported = true, stats.Cost
				return
			}
			s.finish(stats)
		}
	}
}

func (s *piSession) onMessage(m *piMessage) {
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			// The verdict lives in the agent's last message, so a later one
			// (without a line of its own) drops what an earlier one said.
			text, done, bad := parseDone(strings.TrimSpace(b.Text))
			s.done, s.badDone = done, bad
			if text = strings.TrimSpace(text); text != "" {
				s.lastText = text
				e := s.entry("text")
				e.Text = text
				s.opts.OnEntry(e)
			}
		case "thinking":
			if text := strings.TrimSpace(b.Thinking); text != "" {
				e := s.entry("thinking")
				e.Text, e.Detail = oneLine(text, 110), clip(text)
				s.opts.OnEntry(e)
			}
		case "toolCall":
			e := s.entry("tool")
			e.ID, e.Name = b.ID, b.Name
			e.Text, e.Arg = toolBits(b.Name, b.Arguments)
			e.Req = reqFields(b.Arguments)
			if b.ID != "" {
				s.pending[b.ID] = e
			}
			s.opts.OnEntry(e)
		}
	}
	// A request that failed for good — pi retries the ones worth retrying — is
	// the turn failing, and the message carries the only word on why.
	if m.StopReason == "error" {
		s.failed = true
		e := s.entry("note")
		e.Text, e.Error = oneLine(m.ErrorMessage, 200), true
		s.opts.OnEntry(e)
	}
}

// piStats is what pi accounts a session at: its cost so far, and how much of
// the window the conversation now takes. Its tokens are unknown right after a
// compaction, until a request has been made on the summary.
type piStats struct {
	Cost         float64 `json:"cost"`
	ContextUsage *struct {
		Tokens *int64 `json:"tokens"`
	} `json:"contextUsage"`
}

// finish ends the turn on the session stats pi answered with.
func (s *piSession) finish(stats piStats) {
	s.turnActive.Store(false)
	delta := max(0, stats.Cost-s.costReported)
	s.costReported = max(s.costReported, stats.Cost)
	var context int64
	if stats.ContextUsage != nil && stats.ContextUsage.Tokens != nil {
		context = *stats.ContextUsage.Tokens
	}
	e := s.entry("result")
	e.Error = s.failed
	if s.failed {
		e.Text = "turn failed"
	} else {
		e.Text = "turn finished"
	}
	e.Text += " · " + turnLine(time.Since(s.started).Seconds(), delta, context)
	s.opts.OnEntry(e)
	end := TurnEnd{Cost: delta, Context: context, Summary: s.summary, Idle: s.idle, Failed: s.failed,
		Text: s.lastText, Done: s.done, Bad: s.badDone}
	s.summary, s.lastText, s.done, s.badDone, s.failed, s.idle = 0, "", nil, "", false, false
	s.opts.OnTurnEnd(end)
}
