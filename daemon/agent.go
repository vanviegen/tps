package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// A live agent process inside a task's container, talking JSON on both ends:
// user/steering messages go in over stdin, events come out and are condensed
// into chat entries. The process stays alive between turns, so follow-up
// messages are instant; after it dies, Resume picks the conversation back up
// from the state in the mounted state directory. What its events look like is
// claude's (see claude.go).

const killScript = `
p=$(cat /tmp/tps-agent.pid 2>/dev/null) && [ -n "$p" ] || exit 0
kill -TERM "$p" 2>/dev/null || exit 0
for i in $(seq 1 30); do kill -0 "$p" 2>/dev/null || exit 0; sleep 0.1; done
kill -KILL "$p" 2>/dev/null || true
`

// ChatEntry is one condensed line of a task's chat log. A tool call and its
// result share a single entry: the result is merged in later, addressed by ID.
type ChatEntry struct {
	K         string     `json:"k"`                   // user | text | thinking | tool | note | result | mark | task
	ID        string     `json:"id,omitempty"`        // tool_use id; an entry re-sent with a known id replaces the original
	Text      string     `json:"text,omitempty"`      // markdown (user/text/note), or a one-line brief (thinking/tool/result)
	Name      string     `json:"name,omitempty"`      // tool name
	Arg       string     `json:"arg,omitempty"`       // tool: the main argument, one line
	Res       string     `json:"res,omitempty"`       // tool: the result, one line
	Req       []ReqField `json:"req,omitempty"`       // tool: the full request, field by field
	Detail    string     `json:"detail,omitempty"`    // full text (thinking/note)
	Service   string     `json:"service,omitempty"`   // note: the service whose run it is about, whose console it opens
	Run       int64      `json:"run,omitempty"`       // note: when that run started, which tells it from later ones
	ResDetail string     `json:"resDetail,omitempty"` // full result text
	Error     bool       `json:"error,omitempty"`
	Rev       bool       `json:"rev,omitempty"`  // said by the reviewer rather than by the task's own agent (see review.go)
	Mark      *Mark      `json:"mark,omitempty"` // mark: the save point this entry is (see mark.go)
	Task      *TaskLink  `json:"task,omitempty"` // task: one made on the task's behalf (see board.go)
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
func (s *Session) entry(k string) *ChatEntry {
	e := newEntry(k)
	e.Rev = s.opts.Review
	return e
}

type SessionOpts struct {
	Container *Container
	Model     string
	System    string
	Resume    bool // pick the task's most recent session back up
	// Review: this session is the reviewer's rather than the task's own (see
	// review.go). It starts blank and is never written down, so the task's own
	// conversation stays the one a later Resume picks back up, and what it
	// says is marked as its in the log.
	Review   bool
	Budget   *float64 // USD this session may spend, where the CLI can be told
	OnEntry  func(e *ChatEntry)
	OnUpdate func(e *ChatEntry) // an earlier entry (matched by id) changed
	// OnTurnStart: the agent set to work on a turn. A message sent while a turn
	// runs steers that turn; one sent while the agent compacts is queued, and
	// starts as a turn of its own the moment the compaction ends — which is
	// what this hears of, the send having been long ago.
	OnTurnStart func()
	OnTurnEnd   func(end TurnEnd) // a turn ended: cost, and the message the agent ended on
	OnExit      func(code int, errTail string)
}

// Session is a task's conversation with a running agent: a podman exec into
// the task's container, fed JSON lines on stdin and read line by line.
type Session struct {
	turnActive atomic.Bool
	stopping   atomic.Bool
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	writeMu    sync.Mutex
	errMu      sync.Mutex
	errTail    string
	opts       SessionOpts
	exited     chan struct{}

	costReported float64               // cumulative session cost of the last result event
	pending      map[string]*ChatEntry // tool calls awaiting their result
	context      int64                 // tokens the last request sent, which is what claude currently remembers
	summary      int64                 // tokens the turn under way summarised the conversation down to (see TurnEnd)
	lastText     string                // the latest message of the turn under way (see TurnEnd)
	limited      bool                  // that message was claude reporting a usage limit
	limitAt      time.Time             // and when it says that limit resets
	noLogin      bool                  // the turn ran into claude being unable to authenticate at all
}

// newSession starts claude in the task's container, with the environment its
// start script reads, and reads its events until it exits.
func newSession(opts SessionOpts, env []string) (*Session, error) {
	args := []string{"exec", "-i"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, opts.Container.Name, "bash", "-lc", claudeScript)
	cmd := exec.Command("podman", args...)
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
	s := &Session{cmd: cmd, stdin: stdin, stdout: stdout, opts: opts, exited: make(chan struct{}), pending: map[string]*ChatEntry{}}
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
	s.run()
	return s, nil
}

// run reads claude's events, one line at a time, until it exits.
func (s *Session) run() {
	go func() {
		sc := bufio.NewScanner(s.stdout)
		sc.Buffer(make([]byte, 1<<20), 256<<20)
		for sc.Scan() {
			// The turn being stopped has ended: a message that came in during it
			// would start the next one, and nothing of that is wanted.
			if s.stopping.Load() && !s.turnActive.Load() {
				continue
			}
			if line := strings.TrimSpace(sc.Text()); line != "" {
				s.onLine([]byte(line))
			}
		}
		code := exitCode(s.cmd.Wait())
		s.turnActive.Store(false)
		s.errMu.Lock()
		tail := strings.TrimSpace(s.errTail)
		s.errMu.Unlock()
		s.opts.OnExit(code, tail)
		close(s.exited)
	}()
}

func (s *Session) TurnActive() bool { return s.turnActive.Load() }

// Kill ends just the client process (the container side dies with the container).
func (s *Session) Kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func (s *Session) write(msg any) {
	raw, _ := json.Marshal(msg)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.stdin.Write(append(raw, '\n'))
}

// --- what a turn amounts to ---

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

// turnLine is the one-line result every turn ends the log with: how long it
// took, what it cost, and how full the agent's head is now.
func turnLine(secs float64, cost float64, context int64) string {
	line := ""
	if secs > 0 {
		line += fmt.Sprintf("%ds", int(secs+0.5))
	}
	if cost > 0 {
		line += fmt.Sprintf(" · $%.2f", cost)
	}
	if context > 0 {
		line += " · " + tokens(context) + " in context"
	}
	return line
}

// compactNote is what a compaction note says when it is opened: the two
// things about it that are not obvious from the line itself.
const compactNote = `The two figures are the agent's own report of the compaction, not the size of its window ` +
	`before and after; what it really holds is what the ring beside the message box shows, from the next turn on.

The agent has forgotten, but the task has not: it only ever adds to its transcript, so the save points ` +
	`above still measure the whole conversation. Putting the task back to one — or forking from it — gives the ` +
	`agent its memory back as it was.`

func str(v any) string {
	s, _ := v.(string)
	return s
}

// first of these keys the input has, for tools we know nothing else about.
func firstOf(input map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := input[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// toolBits gives a tool call's description and main argument, for the one-line
// rendering.
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
		a = firstOf(input, "file_path", "path", "pattern", "url", "query", "prompt", "command")
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
	return slices.Sorted(maps.Keys(m))
}

// --- the end of a turn ---

// TurnEnd is what a finished agent turn amounts to for the task.
type TurnEnd struct {
	Cost    float64   // USD spent since the previous turn
	Context int64     // tokens the turn's last request sent: what the agent remembers now, or 0 when it compacted and sent nothing since
	Summary int64     // tokens the turn summarised the conversation down to, when it compacted it
	Idle    bool      // the model was never called: a /compact's turn, in which nothing was asked of the agent
	Failed  bool      // the agent reported the turn itself as failed
	Text    string    // the last message of the turn: the reviewer's answer (see review.go), or the agent's status line (see statusOf)
	Limited bool      // the last message was the agent reporting a usage limit
	LimitAt time.Time // when that limit resets; zero when it named no time we could read
	NoLogin bool      // the turn ran into the agent not being able to authenticate (see authGone)
}

// The statuses an agent ends every turn on (see turnRule): what is to become
// of the task now.
const (
	turnDone             = "done"               // nothing to merge, and nothing asked of anyone
	turnMerge            = "merge"              // the work is ready, .tps-commit-message says as what
	turnMergeAndContinue = "merge-and-continue" // the same, and the agent carries on after the merge (see landed)
	turnBlocked          = "blocked"            // a question or a blocker for the user, which the message explains
)

// statusRe is the last paragraph of the message an agent ends every turn
// with: its status, and one sentence on what the turn did.
var statusRe = regexp.MustCompile(`^\[(done|merge|merge-and-continue|blocked)\]:[*_]*\s+(.+)$`)

// statusOf reads the status and that sentence out of the turn's last message,
// "" for both when its last paragraph is not one.
func statusOf(text string) (status, summary string) {
	paras := strings.Split(strings.TrimSpace(text), "\n\n")
	m := statusRe.FindStringSubmatch(strings.Trim(strings.TrimSpace(paras[len(paras)-1]), "*_"))
	if m == nil {
		return "", ""
	}
	return m[1], strings.TrimSpace(strings.Trim(m[2], " *_"))
}
