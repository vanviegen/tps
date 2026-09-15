package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// A live `claude` process inside a task's container, talking stream-json on
// both ends: user/steering messages go in over stdin, events come out and are
// condensed into chat entries. The process stays alive between turns, so
// follow-up messages are instant; after it dies, Resume (--continue) picks
// the conversation back up from the state in the mounted claude dir.

const startScript = `
mkdir -p "$CLAUDE_CONFIG_DIR"
export PATH=/tps/bin:$PATH
echo $$ >/tmp/tps-agent.pid
exec /tps/bin/claude -p --input-format stream-json --output-format stream-json --verbose \
	--dangerously-skip-permissions ${TPS_MODEL:+--model "$TPS_MODEL"} --append-system-prompt "$TPS_SYSTEM" \
	${TPS_BUDGET:+--max-budget-usd "$TPS_BUDGET"} $TPS_EXTRA
`

const killScript = `
p=$(cat /tmp/tps-agent.pid 2>/dev/null) && [ -n "$p" ] || exit 0
kill -TERM "$p" 2>/dev/null || exit 0
for i in $(seq 1 30); do kill -0 "$p" 2>/dev/null || exit 0; sleep 0.1; done
kill -KILL "$p" 2>/dev/null || true
`

// ChatEntry is one condensed line of a task's chat log. A tool call and its
// result share a single entry: the result is merged in later, addressed by ID.
type ChatEntry struct {
	K         string     `json:"k"`                   // user | text | thinking | tool | note | result | mark
	ID        string     `json:"id,omitempty"`        // tool_use id; an entry re-sent with a known id replaces the original
	Text      string     `json:"text,omitempty"`      // markdown (user/text/note), or a one-line brief (thinking/tool/result)
	Name      string     `json:"name,omitempty"`      // tool name
	Arg       string     `json:"arg,omitempty"`       // tool: the main argument, one line
	Res       string     `json:"res,omitempty"`       // tool: the result, one line
	Req       []ReqField `json:"req,omitempty"`       // tool: the full request, field by field
	Detail    string     `json:"detail,omitempty"`    // full text (thinking/note)
	ResDetail string     `json:"resDetail,omitempty"` // full result text
	Error     bool       `json:"error,omitempty"`
	Rev       bool       `json:"rev,omitempty"`  // said by the reviewer rather than by the task's own agent (see review.go)
	Mark      *Mark      `json:"mark,omitempty"` // mark: the save point this entry is (see mark.go)
	T         int64      `json:"t"`
}

// ReqField is one key/value of a tool call's input, as the detail dialog rows it.
type ReqField struct {
	K string `json:"k"`
	V string `json:"v"`
}

func newEntry(k string) *ChatEntry {
	return &ChatEntry{K: k, T: time.Now().UnixMilli()}
}

// entry is newEntry for what this session says, which is the reviewer's or the
// task's agent's own.
func (s *ChatSession) entry(k string) *ChatEntry {
	e := newEntry(k)
	e.Rev = s.opts.Review
	return e
}

type SessionOpts struct {
	Container *Container
	Model     string
	System    string
	Resume    bool // --continue the task's most recent session
	// Review: this session is the reviewer's rather than the task's own (see
	// review.go). It starts blank and is never written down, so the task's own
	// conversation stays the one a later --continue picks back up, and what it
	// says is marked as its in the log.
	Review    bool
	Budget    *float64 // USD this session may spend (--max-budget-usd)
	OnEntry   func(e *ChatEntry)
	OnUpdate  func(e *ChatEntry) // an earlier entry (matched by id) changed
	OnTurnEnd func(end TurnEnd)  // a result event arrived: cost, and the verdict the agent ended on
	OnExit    func(code int, errTail string)
}

type ChatSession struct {
	turnActive   atomic.Bool
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	writeMu      sync.Mutex
	errMu        sync.Mutex
	errTail      string
	costReported float64               // cumulative session cost of the last result event
	pending      map[string]*ChatEntry // tool calls awaiting their result
	context      int64                 // tokens the last request sent, which is what claude currently remembers
	lastText     string                // the latest message of the turn under way, which is the reviewer's answer (see review.go)
	done         *Done                 // the verdict of the turn under way, from its latest message
	badDone      string                // why that message's TPS-DONE line was unusable
	limited      bool                  // that message was claude reporting a usage limit
	limitAt      time.Time             // and when it says that limit resets
	noLogin      bool                  // the turn ran into claude being unable to authenticate at all
	opts         SessionOpts
	exited       chan struct{}
}

func newChatSession(opts SessionOpts) (*ChatSession, error) {
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
	if model == DefaultModel {
		model = ""
	}
	cmd := exec.Command("podman", "exec", "-i",
		"-e", "TZ=UTC", // the zone claude words its usage-limit resets in; see parseReset
		"-e", "TPS_MODEL="+model,
		"-e", "TPS_SYSTEM="+opts.System,
		"-e", "TPS_EXTRA="+extra,
		"-e", "TPS_BUDGET="+budget,
		opts.Container.Name, "bash", "-lc", startScript)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &ChatSession{cmd: cmd, stdin: stdin, pending: map[string]*ChatEntry{}, opts: opts, exited: make(chan struct{})}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				s.errMu.Lock()
				s.errTail += string(buf[:n])
				if len(s.errTail) > 4000 {
					s.errTail = s.errTail[len(s.errTail)-4000:]
				}
				s.errMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 256<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var ev event
			if json.Unmarshal([]byte(line), &ev) == nil {
				s.onEvent(&ev)
			}
		}
		code := exitCode(cmd.Wait())
		s.turnActive.Store(false)
		s.errMu.Lock()
		tail := strings.TrimSpace(s.errTail)
		s.errMu.Unlock()
		opts.OnExit(code, tail)
		close(s.exited)
	}()
	return s, nil
}

func (s *ChatSession) TurnActive() bool { return s.turnActive.Load() }

func (s *ChatSession) Send(text string) {
	s.turnActive.Store(true)
	s.lastText = ""
	s.limited, s.limitAt, s.noLogin = false, time.Time{}, false
	s.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}}})
}

// Stop interrupts the current turn, then makes sure the process is gone.
// Returns only after the exit was processed (OnExit has run).
func (s *ChatSession) Stop() {
	s.write(map[string]any{"type": "control_request", "request_id": "stop", "request": map[string]any{"subtype": "interrupt"}})
	for i := 0; i < 40 && s.turnActive.Load(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	// The podman exec client can't signal into the container; kill from within.
	go s.opts.Container.Exec(killScript)
	time.Sleep(300 * time.Millisecond)
	s.Kill()
	<-s.exited
}

// Kill ends just the client process (the container side dies with the container).
func (s *ChatSession) Kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func (s *ChatSession) write(msg any) {
	raw, _ := json.Marshal(msg)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.stdin.Write(append(raw, '\n'))
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

func (s *ChatSession) onEvent(ev *event) {
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
				// The verdict lives in the agent's last message, so a later one
				// (without a line of its own) drops what an earlier one said.
				text, done, bad := parseDone(strings.TrimSpace(b.Text))
				s.done, s.badDone = done, bad
				s.limited, s.limitAt = limitOf(b.Text)
				s.noLogin = s.noLogin || authGone(b.Text)
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
		// Compaction: claude replacing everything it remembers with a summary
		// of it, either because the context window filled up or because
		// somebody said /compact. It only ever appends to its transcript, so
		// the save points made before this one still measure the whole
		// conversation and reverting to one undoes the compaction (see mark).
		switch {
		case ev.Subtype == "compact_boundary" && ev.CompactMeta != nil:
			m := ev.CompactMeta
			how := "claude's context filled up, so it was compacted"
			if m.Trigger == "manual" {
				how = "claude compacted its context on request"
			}
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
		secs, cost, held, why := "", "", "", ""
		if ev.DurationMS > 0 {
			secs = fmt.Sprintf("%ds", int(ev.DurationMS/1000+0.5))
		}
		if delta > 0 {
			cost = fmt.Sprintf(" · $%.2f", delta)
		}
		if s.context > 0 {
			held = " · " + tokens(s.context) + " in context"
		}
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
		e.Text += " · " + secs + cost + held
		s.opts.OnEntry(e)
		end := TurnEnd{Cost: delta, Context: s.context, Failed: ev.IsError, Text: s.lastText, Done: s.done, Bad: s.badDone, Limited: s.limited, LimitAt: s.limitAt, NoLogin: s.noLogin}
		s.lastText, s.done, s.badDone, s.limited, s.limitAt, s.noLogin = "", nil, "", false, time.Time{}, false
		s.opts.OnTurnEnd(end)
	}
}

// resultText flattens a tool result's content (a string or content blocks).
func resultText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, len(blocks))
		for i, b := range blocks {
			if b.Type == "text" {
				parts[i] = b.Text
			} else {
				parts[i] = "[" + b.Type + "]"
			}
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}

var spaces = regexp.MustCompile(`\s+`)

func oneLine(value any, maxLen int) string {
	s, ok := value.(string)
	if !ok && value != nil {
		raw, _ := json.Marshal(value)
		s = string(raw)
	}
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) > maxLen {
		r := []rune(s)
		return string(r[:maxLen-1]) + "…"
	}
	return s
}

func clip(s string) string {
	const maxLen = 16_000
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… (%d more characters)", len(s)-cut)
}

// tokens renders a token count the way the log mentions it: 950, 9k, 23k, 1.2M.
func tokens(n int64) string {
	round := func(v float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") }
	switch {
	case n >= 1_000_000:
		return round(float64(n)/1e6) + "M"
	case n >= 10_000:
		return strconv.FormatInt(n/1000, 10) + "k"
	case n >= 1_000:
		return round(float64(n)/1e3) + "k"
	}
	return strconv.FormatInt(n, 10)
}

// compactNote is what the compaction note says when it is opened: the two
// things about it that are not obvious from the line itself.
const compactNote = `The two figures are claude's own report of the compaction, not the size of its window ` +
	`before and after; what it really holds is what the ring beside the message box shows, from the next turn on.

The agent has forgotten, but the task has not: claude only ever adds to its transcript, so the save points ` +
	`above still measure the whole conversation. Putting the task back to one — or forking from it — gives the ` +
	`agent its memory back as it was.`

func str(v any) string {
	s, _ := v.(string)
	return s
}

// toolBits gives a tool call's description and main argument, for the one-line rendering.
func toolBits(name string, input map[string]any) (text, arg string) {
	text = str(input["description"])
	var a any
	switch name {
	case "Bash":
		a = input["command"]
	case "Edit":
		a = fmt.Sprintf("%s: %s → %s", str(input["file_path"]), oneLine(input["old_string"], 40), oneLine(input["new_string"], 40))
	case "Write":
		a = fmt.Sprintf("%s ← %s", str(input["file_path"]), oneLine(input["content"], 60))
	case "Grep":
		a = str(input["pattern"])
		if p := str(input["path"]); p != "" {
			a = a.(string) + " in " + p
		}
	default:
		for _, k := range []string{"file_path", "path", "pattern", "url", "query", "prompt"} {
			if v, ok := input[k]; ok && v != nil {
				a = v
				break
			}
		}
		if a == nil {
			var parts []string
			for _, k := range sortedKeys(input) {
				if k != "description" {
					parts = append(parts, k+": "+oneLine(input[k], 40))
				}
			}
			a = strings.Join(parts, ", ")
		}
	}
	return text, oneLine(a, 110)
}

// reqFields renders tool input as the fields the detail dialog tabulates:
// strings as they are, anything else as indented JSON.
func reqFields(input map[string]any) []ReqField {
	fields := make([]ReqField, 0, len(input))
	for _, k := range sortedKeys(input) {
		s, ok := input[k].(string)
		if !ok {
			raw, _ := json.MarshalIndent(input[k], "", "  ")
			s = string(raw)
		}
		fields = append(fields, ReqField{K: k, V: clip(s)})
	}
	return fields
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- the TPS-DONE line ---

// doneMarker opens the line an agent ends every turn with: where the task
// goes next, as JSON. See systemPrompt.
const doneMarker = "TPS-DONE:"

// Done is an agent's verdict on its turn.
type Done struct {
	Next    string `json:"next"`              // user | merge | reload
	Message string `json:"message,omitempty"` // the commit message, with next=merge
	Changes string `json:"changes,omitempty"` // what this turn changed, for the save point's commit (see mark)
}

// TurnEnd is what a finished claude turn amounts to for the task.
type TurnEnd struct {
	Cost    float64   // USD spent since the previous turn
	Context int64     // tokens the turn's last request sent: what claude remembers now
	Failed  bool      // claude reported the turn itself as failed
	Text    string    // the last message of the turn: the reviewer's answer (see review.go)
	Done    *Done     // the verdict, if the last message carried a usable one
	Bad     string    // why a TPS-DONE line that was there could not be used
	Limited bool      // the last message was claude reporting a usage limit
	LimitAt time.Time // when that limit resets; zero when it named no time we could read
	NoLogin bool      // the turn ran into claude not being able to authenticate (see authGone)
}

// parseDone splits a TPS-DONE line off the end of an agent message: the text
// without it, the verdict, and (if a line was there but unusable) what is
// wrong with it. The last marker in the message wins, and the JSON runs from
// it to the end of the message, so a verdict spread over several lines still
// reads while an example line quoted mid-message does not count as one.
func parseDone(text string) (rest string, done *Done, bad string) {
	lines := strings.Split(text, "\n")
	at := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), doneMarker) {
			at = i
		}
	}
	if at < 0 {
		return text, nil, ""
	}
	before := strings.Join(lines[:at], "\n")
	tail := lines[at:]
	// A fence the agent wrapped the line in is none of the JSON.
	for len(tail) > 0 && isFence(tail[len(tail)-1]) {
		tail = tail[:len(tail)-1]
	}
	if b := strings.Split(before, "\n"); len(b) > 0 && isFence(b[len(b)-1]) {
		before = strings.Join(b[:len(b)-1], "\n")
	}
	rest = strings.TrimSpace(before)
	raw := strings.TrimSpace(strings.Join(tail, "\n"))
	raw = strings.TrimSpace(strings.TrimPrefix(raw, doneMarker))
	var d Done
	if json.Unmarshal([]byte(raw), &d) != nil {
		return rest, nil, "the JSON after it could not be read"
	}
	switch d.Next {
	case "user", "merge", "reload":
		return rest, &d, ""
	case "":
		return rest, nil, "it has no 'next'"
	}
	return rest, nil, "'" + oneLine(d.Next, 30) + "' is not one of user, merge, reload"
}

func isFence(line string) bool {
	line = strings.TrimSpace(line)
	return line == "" || strings.HasPrefix(line, "```")
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
// UTC because that is the zone the process is given (see newChatSession) —
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
