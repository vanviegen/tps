package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// The claude provider: Claude Code, run in a task's container as one long
// `claude -p` talking stream-json on both ends (see agent.go), and asked on
// the host for the models it takes and for a task's title.

const claudeVersion = "2.1.280"

// claudeMount is the task's own claude directory in its container; authMount
// beside it is the daemon's, the login every task shares (see login.go).
const (
	claudeMount = "/claude"
	authMount   = "/claude-auth"
)

// A turn of claude's ends where a run of the task's does, and nothing may
// come between the two: work claude keeps of its own — a backgrounded command,
// a Monitor stream, a scheduled wakeup — ends the turn and then starts another
// one by itself, long after TPS made the run's save point and handed the task
// back. So every way it has of doing that is taken away, and what takes a
// while is a TPS service instead (see guesttool.go), which the user can see
// and the task does not wait on.
//
// CLAUDE_CODE_DISABLE_BACKGROUND_TASKS (see Start) takes the run_in_background
// parameter off Bash and off subagents, and stops a command that hits its
// timeout being moved to the background rather than killed; the tools denied
// below are the ones that schedule or stream on their own.
const claudeScript = `
mkdir -p "$CLAUDE_CONFIG_DIR"
export PATH=/tps/bin:$PATH
echo $$ >/tmp/tps-agent.pid
exec /tps/bin/claude -p --input-format stream-json --output-format stream-json --verbose \
	--dangerously-skip-permissions ${TPS_MODEL:+--model "$TPS_MODEL"} --append-system-prompt "$TPS_SYSTEM" \
	--disallowed-tools "Monitor CronCreate CronDelete CronList ScheduleWakeup Workflow" \
	${TPS_BUDGET:+--max-budget-usd "$TPS_BUDGET"} $TPS_EXTRA
`

type claudeCLI struct{}

func (claudeCLI) Name() string    { return "claude" }
func (claudeCLI) Version() string { return claudeVersion }

func (claudeCLI) Install(dir string) error {
	arch, ok := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("no claude build for %s", runtime.GOARCH)
	}
	url := "https://downloads.claude.ai/claude-code-releases/" + claudeVersion + "/linux-" + arch + "/claude"
	return fetch(url, func(r io.Reader) error {
		return writeFile(filepath.Join(dir, "bin", "claude"), r, 0o755)
	})
}

// Mounts: the task's own claude directory as its config dir, and the host's
// claude login, shared with every task (see login.go). The daemon's claude
// directory is where claude in the container keeps its credentials, while its
// config dir stays the task's own. The directory, not the one file, as claude
// writes it by replacing it and locks beside it.
func (claudeCLI) Mounts(dir string) []string {
	args := []string{
		"-v", dir + ":" + claudeMount,
		"-e", "CLAUDE_CONFIG_DIR=" + claudeMount,
		"-e", "DISABLE_AUTOUPDATER=1", // the toolbox is read-only, and versioned by TPS
		"-v", authDir() + ":" + authMount,
		"-e", "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + authMount,
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		args = append(args, "-e", "ANTHROPIC_API_KEY")
	}
	return args
}

func (claudeCLI) Transcripts() string { return filepath.Join("projects", "*", "*.jsonl") }

func (claudeCLI) Window(c *Container, model, system string) (ContextWindow, bool) {
	return probeContext(c, model, system)
}

func (claudeCLI) Start(opts SessionOpts) (Session, error) {
	extra, budget := "", ""
	if opts.Resume {
		extra = "--continue"
	}
	if opts.Review {
		// Nobody's conversation: it starts blank and is never written to the
		// claude dir, so the session a later --continue picks back up is still
		// the task's own.
		extra = "--no-session-persistence"
	}
	if opts.Budget != nil {
		budget = fmt.Sprintf("%.2f", *opts.Budget)
	}
	// The default model is claude's own: pass no --model at all.
	model := opts.Model
	if model == defaultModel {
		model = ""
	}
	env := []string{
		"TZ=UTC", // the zone claude words its usage-limit resets in; see parseReset
		"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1",
		"TPS_MODEL=" + model,
		"TPS_SYSTEM=" + opts.System,
		"TPS_EXTRA=" + extra,
		"TPS_BUDGET=" + budget,
	}
	p, err := newAgentProc(opts, env, claudeScript)
	if err != nil {
		return nil, err
	}
	s := &claudeSession{agentProc: p, pending: map[string]*ChatEntry{}}
	p.run(s.onLine)
	return s, nil
}

type claudeSession struct {
	*agentProc
	costReported float64               // cumulative session cost of the last result event
	pending      map[string]*ChatEntry // tool calls awaiting their result
	context      int64                 // tokens the last request sent, which is what claude currently remembers
	summary      int64                 // tokens the turn under way summarised the conversation down to (see TurnEnd)
	lastText     string                // the latest message of the turn under way (see TurnEnd)
	limited      bool                  // that message was claude reporting a usage limit
	limitAt      time.Time             // and when it says that limit resets
	noLogin      bool                  // the turn ran into claude being unable to authenticate at all
}

func (s *claudeSession) Send(text string) {
	s.turnActive.Store(true)
	s.lastText = ""
	s.limited, s.limitAt, s.noLogin = false, time.Time{}, false
	s.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}}})
}

func (s *claudeSession) Stop() {
	s.stop(map[string]any{"type": "control_request", "request_id": "stop", "request": map[string]any{"subtype": "interrupt"}})
}

// --- stream-json events → chat entries ---

type event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message *struct {
		Content []block `json:"content"`
		Usage   *usage  `json:"usage"`
	} `json:"message"`
	Result       string   `json:"result"` // why a failed turn failed, where claude says it here rather than in a message
	TotalCostUSD *float64 `json:"total_cost_usd"`
	DurationMS   float64  `json:"duration_ms"`
	IsError      bool     `json:"is_error"`
	NumTurns     int      `json:"num_turns"` // result: how often the turn called the model
	// A system event about compaction: the boundary one carries the metadata,
	// a status one that failed says why (see onEvent).
	CompactMeta  *compactMeta `json:"compact_metadata"`
	CompactRes   string       `json:"compact_result"`
	CompactError string       `json:"compact_error"`
}

// usage is what one request to claude was billed for, in tokens. The three
// input figures together are the context it sent: what was fresh, what was
// freshly cached, and what was read back from cache.
type usage struct {
	Input         int64 `json:"input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

func (u *usage) context() int64 { return u.Input + u.CacheCreation + u.CacheRead }

// compactMeta is what claude reports about a compaction it just did.
type compactMeta struct {
	Trigger string `json:"trigger"` // auto: the window filled up · manual: someone said /compact
	Pre     int64  `json:"pre_tokens"`
	Post    int64  `json:"post_tokens"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // a string, or a list of content blocks
	IsError   bool            `json:"is_error"`
}

func (s *claudeSession) onLine(line []byte) {
	var ev event
	if json.Unmarshal(line, &ev) == nil {
		s.onEvent(&ev)
	}
}

func (s *claudeSession) onEvent(ev *event) {
	switch ev.Type {
	case "assistant":
		if ev.Message == nil {
			return
		}
		// Every message says what the request that produced it sent, which is
		// the size of the conversation as claude now holds it.
		if ev.Message.Usage != nil {
			if n := ev.Message.Usage.context(); n > 0 {
				s.context = n
			}
		}
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "text":
				text := strings.TrimSpace(b.Text)
				s.limited, s.limitAt = limitOf(b.Text)
				s.noLogin = s.noLogin || authGone(b.Text)
				if text != "" {
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
			case "tool_use":
				e := s.entry("tool")
				e.ID, e.Name = b.ID, b.Name
				e.Text, e.Arg = toolBits(b.Name, b.Input)
				e.Req = reqFields(b.Input)
				if b.ID != "" {
					s.pending[b.ID] = e
				}
				s.opts.OnEntry(e)
			}
		}
	case "user":
		if ev.Message == nil {
			return
		}
		for _, b := range ev.Message.Content {
			if b.Type != "tool_result" {
				continue
			}
			text := resultText(b.Content)
			if text == "" {
				text = "(no output)"
			}
			if e := s.pending[b.ToolUseID]; e != nil && b.ToolUseID != "" {
				delete(s.pending, b.ToolUseID)
				e.Res, e.ResDetail, e.Error = oneLine(text, 140), clip(resultText(b.Content)), b.IsError
				s.opts.OnUpdate(e)
			} else { // result without a tracked call (shouldn't normally happen)
				e := s.entry("tool")
				e.Name, e.Res, e.ResDetail, e.Error = "result", oneLine(text, 140), clip(resultText(b.Content)), b.IsError
				s.opts.OnEntry(e)
			}
		}
	case "system":
		switch {
		case ev.Subtype == "init":
			s.turnActive.Store(true)
			s.opts.OnTurnStart()
		// Compaction: claude replacing everything it remembers with a summary
		// of it, either because the context window filled up or because
		// somebody said /compact. It only ever appends to its transcript, so
		// the save points made before this one still measure the whole
		// conversation and reverting to one undoes the compaction (see mark).
		case ev.Subtype == "compact_boundary" && ev.CompactMeta != nil:
			m := ev.CompactMeta
			how := "claude's context filled up, so it was compacted"
			if m.Trigger == "manual" {
				how = "claude compacted its context on request"
			}
			// What the last request sent is no longer what claude holds: the
			// next request measures that, and until one is made the summary
			// is the whole of the conversation.
			s.context, s.summary = 0, m.Post
			e := s.entry("note")
			// Claude's own two figures, as it words them. They are not the size
			// of the window before and after — a request made right after a
			// compaction sends far more than the second one — so they are given
			// as what they are and nothing is read into them (see compactNote).
			e.Text = fmt.Sprintf("%s: %s → %s", how, tokens(m.Pre), tokens(m.Post))
			e.Detail = compactNote
			s.opts.OnEntry(e)
		case ev.CompactRes == "failed":
			e := s.entry("note")
			e.Text, e.Error = "compacting claude's context failed", true
			if why := strings.TrimSpace(ev.CompactError); why != "" {
				e.Text += ": " + oneLine(why, 200)
			}
			s.opts.OnEntry(e)
		}
	case "result":
		s.turnActive.Store(false)
		// A turn that went fine carries the agent's own last words here, which
		// may well be about logins: only a failed one is read for the reason.
		s.noLogin = s.noLogin || (ev.IsError && authGone(ev.Result))
		total := s.costReported
		if ev.TotalCostUSD != nil {
			total = *ev.TotalCostUSD
		}
		delta := max(0, total-s.costReported)
		s.costReported = total
		why := ""
		if ev.IsError && ev.Subtype != "" && ev.Subtype != "success" { // an auth failure comes labeled 'success'
			why = " (" + strings.ReplaceAll(strings.TrimPrefix(ev.Subtype, "error_"), "_", " ") + ")"
		}
		e := s.entry("result")
		e.Error = ev.IsError
		if ev.IsError {
			e.Text = "turn failed" + why
		} else {
			e.Text = "turn finished"
		}
		e.Text += " · " + turnLine(ev.DurationMS/1000, delta, s.context)
		s.opts.OnEntry(e)
		end := TurnEnd{Cost: delta, Context: s.context, Summary: s.summary, Idle: ev.NumTurns == 0, Failed: ev.IsError,
			Text: s.lastText, Limited: s.limited, LimitAt: s.limitAt, NoLogin: s.noLogin}
		s.summary, s.lastText, s.limited, s.limitAt, s.noLogin = 0, "", false, time.Time{}, false
		s.opts.OnTurnEnd(end)
	}
}

// --- the models claude offers, and what it calls a task ---

// The models a task can pick under "claude" are the ones the claude CLI on
// this host says it takes: `claude -p /model` is a local slash command (no API
// call) that prints them.

var availableRe = regexp.MustCompile(`\bAvailable:[ \t]*([^\n]+)`)
var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:\[\]/-]*$`)

func (claudeCLI) Models() ([]string, error) {
	r, err := runCmd(askArgs("/model"), RunOpts{Dir: home(), Env: askEnv(), Timeout: 60 * time.Second})
	models := parseModels(r.Out + "\n" + r.Err)
	if models == nil {
		if err == nil {
			err = fmt.Errorf("no model list in `%s`", firstLine(r.Out+r.Err))
		}
		return nil, err
	}
	return models, nil
}

// parseModels picks the model names out of `claude -p /model` output:
//
//	Usage: /model <name>. Available: sonnet, opus, …, default, or a full model ID.
//
// Whatever is not a bare name (the trailing "or a full model ID") is dropped,
// as is "default", which every provider's list leads with anyway.
func parseModels(out string) []string {
	m := availableRe.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	var models []string
	seen := map[string]bool{defaultModel: true}
	for _, part := range strings.Split(m[1], ",") {
		name := strings.Trim(strings.TrimSpace(part), ".")
		if !modelRe.MatchString(name) || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, name)
	}
	return models
}

// Title asks this host's claude for a task title: haiku, as naming one is
// little work, and a one-shot -p run, as there is nothing to discuss. It
// answers "" when it can't be asked (no toolbox yet, no network) or won't say
// anything usable; the stand-in title then simply stays.
func (claudeCLI) Title(description string) string {
	r, err := runCmd(askArgs(titlePrompt(description), "--model", "haiku"), RunOpts{Dir: home(), Env: askEnv(), Timeout: 30 * time.Second})
	if err != nil {
		logf("asking claude for a task title failed: %v", err)
		return ""
	}
	return titleOf(r.Out)
}

// askArgs: a one-shot errand for this host's claude. Naming a task and asking
// which models there are is no conversation, so everything claude sets up for a
// session it keeps is only in the way. The MCP servers a user has configured
// are the expensive part — connecting to them takes seconds before the first
// token is asked for — and a session nothing will resume needs no saving.
func askArgs(args ...string) []string {
	argv := append([]string{claudeBin(), "-p"}, args...)
	return append(argv, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence")
}

// askEnv gives the run the daemon's claude dir (see login.go) and drops the two model
// calls it would make on the side: claude having a model name the session
// behind our back, and thinking. The answers wanted here are one line long,
// and claude spent ten times as many tokens thinking about a title as it did
// saying it. All together, a title now takes about a second, where the full
// session took seven to ten.
func askEnv() []string {
	return append(claudeEnv(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "MAX_THINKING_TOKENS=0")
}

// --- claude's usage limits ---

// A turn that runs into one of claude's usage limits ends with a message from
// claude itself — "You've hit your session limit · resets 4:40pm (UTC)" — and
// a failed result right behind it. TPS reads the reset out of that message and
// waits for it rather than handing the task to the user (see armLimitL).
var (
	limitHit   = regexp.MustCompile(`(?i)you'?ve hit your [a-z' ]*limit\b`)
	limitReset = regexp.MustCompile(`(?i)\bresets ([^·\n]+)`)
)

// limitOf reports whether a message is claude saying a usage limit stopped it,
// and when that limit resets — zero when it names no time, or none we can read.
func limitOf(text string) (bool, time.Time) {
	if !limitHit.MatchString(text) {
		return false, time.Time{}
	}
	m := limitReset.FindStringSubmatch(text)
	if m == nil {
		return true, time.Time{}
	}
	return true, parseReset(strings.TrimSpace(m[1]), time.Now())
}

// --- claude's login ---

// A turn whose credentials no longer work ends much like one that ran into a
// limit: a message from claude itself — "Failed to authenticate: OAuth session
// expired and could not be refreshed" — and a failed result behind it. What
// needs fixing is the host's login rather than anything about the task, so TPS
// reads that out and asks for a sign-in where the user is, instead of leaving
// them to work out what the message meant (see loginExpired). An agent whose
// own words happen to match — this file's would — is why nothing acts on this
// without the turn having failed as well: see onTurnEnd and reviewMissing.
var authWords = regexp.MustCompile(`(?i)failed to authenticate|oauth (session|token) (has )?expired|invalid api key|please run ` + "`?" + `/?(claude )?(auth )?login`)

// authGone reports whether a message is claude saying it could not sign in.
func authGone(text string) bool { return text != "" && authWords.MatchString(text) }

// parseReset reads the clock claude names in that message: "4:40pm (UTC)", in
// UTC because that is the zone the process is given (see claudeCLI.Start) —
// claude words these in its own locale and zone, and only the zone is ours to
// pin. A reset it dates instead ("Mar 5, 4:40pm") is more than a day out,
// which is no wait TPS takes on anyway, so the clock is all that is read.
func parseReset(s string, now time.Time) time.Time {
	s = strings.TrimSuffix(s, " (UTC)")
	for _, layout := range []string{"3:04pm", "3pm"} { // "4:40pm", and "4pm" on the hour
		at, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		day := now.UTC()
		at = time.Date(day.Year(), day.Month(), day.Day(), at.Hour(), at.Minute(), 0, 0, time.UTC)
		if at.Before(day) { // an hour already gone today is tomorrow's
			at = at.AddDate(0, 0, 1)
		}
		return at
	}
	return time.Time{}
}
