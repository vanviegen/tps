package daemon

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vanviegen/tps/hub"
)

// One claude turn, from tool call to result, condensed into chat entries.
func TestSessionEvents(t *testing.T) {
	var entries []*ChatEntry
	var updates []*ChatEntry
	var ends []TurnEnd
	s := &claudeSession{pending: map[string]*ChatEntry{}}
	s.agentProc = &agentProc{opts: SessionOpts{
		OnEntry:   func(e *ChatEntry) { entries = append(entries, e) },
		OnUpdate:  func(e *ChatEntry) { updates = append(updates, e) },
		OnTurnEnd: func(e TurnEnd) { ends = append(ends, e) },
		OnExit:    func(int, string) {},
	}}
	feed := func(line string) {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		s.onEvent(&ev)
	}
	feed(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"  let me   think\nabout it "},{"type":"text","text":"Working on it."},{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/work/a.go","old_string":"x","new_string":"y","description":"Fix a"}}]}}`)
	feed(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"ok\ndone"}],"is_error":false}]}}`)
	feed(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"ls"}}]}}`)
	feed(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"","is_error":true}]}}`)
	feed(`{"type":"result","total_cost_usd":0.0234,"duration_ms":2600,"is_error":false}`)
	feed(`{"type":"result","total_cost_usd":0.03,"duration_ms":500,"is_error":true,"subtype":"error_max_turns"}`)

	if len(entries) != 6 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].K != "thinking" || entries[0].Text != "let me think about it" || !strings.Contains(entries[0].Detail, "\n") {
		t.Errorf("thinking entry: %+v", entries[0])
	}
	if entries[2].K != "tool" || entries[2].Arg != "/work/a.go: x → y" || entries[2].Text != "Fix a" || len(entries[2].Req) != 4 || entries[2].Req[1] != (ReqField{"file_path", "/work/a.go"}) {
		t.Errorf("tool entry: %+v", entries[2])
	}
	if len(updates) != 2 || updates[0] != entries[2] || updates[0].Res != "ok done" || updates[0].ResDetail != "ok\ndone" || updates[0].Error {
		t.Errorf("tool result merge: %+v", updates)
	}
	if updates[1].Res != "(no output)" || !updates[1].Error || updates[1].Arg != "ls" {
		t.Errorf("bash result: %+v", updates[1])
	}
	if entries[4].Text != "turn finished · 3s · $0.02" || len(ends) != 2 || ends[0].Cost != 0.0234 || ends[1].Cost < 0.0065 || ends[1].Cost > 0.0067 {
		t.Errorf("result entry: %q ends %+v", entries[4].Text, ends)
	}
	if ends[0].Failed || !ends[1].Failed {
		t.Errorf("turn ends: %+v", ends)
	}
	if entries[5].Text != "turn failed (max turns) · 1s · $0.01" || !entries[5].Error {
		t.Errorf("failed result: %q", entries[5].Text)
	}
	if s.TurnActive() {
		t.Error("turn still active after result")
	}
	if got := oneLine(strings.Repeat("ab ", 100), 20); len([]rune(got)) != 20 || !strings.HasSuffix(got, "…") {
		t.Errorf("oneLine: %q", got)
	}
}

func TestHelpers(t *testing.T) {
	if len(imageTag(defaultContainerfile)) != len("localhost/tps:")+12 {
		t.Error("image tag")
	}
	if Slugify("My Project!!") != "my-project" || Slugify("--") != "x" {
		t.Error("slugify")
	}
	got := parseNumstat("3\t1\ta.go\x00-\t-\timg.png\x00")
	if len(got) != 2 || got[0] != (change{Path: "a.go", Add: 3, Del: 1}) || !got[1].Bin || got[1].Path != "img.png" {
		t.Errorf("numstat: %+v", got)
	}
}

// The summary an agent ends its turn on: the last paragraph of its last message.
func TestSummary(t *testing.T) {
	cases := []struct{ in, summary string }{
		{"All set.\n\nSummary: Read the config at startup.", "Read the config at startup."},
		{"**Summary:** Fixed the sidebar.", "Fixed the sidebar."},
		{"Summary: one\n\nMore words after it.", ""},
		{"Nothing to see here.", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := summaryOf(c.in); got != c.summary {
			t.Errorf("summaryOf(%q) = %q, want %q", c.in, got, c.summary)
		}
	}
}

// The usage-limit message claude ends a cut-short turn with, and the reset it names.
func TestLimitLine(t *testing.T) {
	now := time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC)
	cases := []struct {
		in    string
		limit bool
		at    time.Time
	}{
		{in: "You've hit your session limit · resets 4:40pm (UTC)", limit: true, at: time.Date(2026, 3, 5, 16, 40, 0, 0, time.UTC)},
		{in: "You've hit your weekly limit · resets 9am (UTC) · ask your admin for a higher limit", limit: true, at: time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC)}, // gone today: tomorrow's
		{in: "You've hit your session limit", limit: true},                            // no time named
		{in: "You've hit your Opus limit · resets Mar 12, 4:40pm (UTC)", limit: true}, // dated, so over a day out: not a wait we take
		{in: "Rate limits reset at 4:40pm; I will explain them.", limit: false},       // the agent talking about limits
	}
	for _, c := range cases {
		limit, _ := limitOf(c.in)
		if limit != c.limit {
			t.Errorf("limitOf(%q) = %v, want %v", c.in, limit, c.limit)
		}
		// The reset itself, read against a fixed 'now' rather than this minute's.
		var at time.Time
		if m := limitReset.FindStringSubmatch(c.in); m != nil && limit {
			at = parseReset(strings.TrimSpace(m[1]), now)
		}
		if !at.Equal(c.at) {
			t.Errorf("parseReset(%q) = %v, want %v", c.in, at, c.at)
		}
	}

	var end TurnEnd
	s := &claudeSession{pending: map[string]*ChatEntry{}}
	s.agentProc = &agentProc{opts: SessionOpts{
		OnEntry:   func(*ChatEntry) {},
		OnUpdate:  func(*ChatEntry) {},
		OnTurnEnd: func(e TurnEnd) { end = e },
		OnExit:    func(int, string) {},
	}}
	feed := func(line string) {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		s.onEvent(&ev)
	}
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"You've hit your session limit · resets 4:40pm (UTC)"}]}}`)
	feed(`{"type":"result","is_error":true,"duration_ms":1000}`)
	if !end.Limited || end.LimitAt.IsZero() || !end.Failed {
		t.Fatalf("limited turn: %+v", end)
	}
	// It does not carry over into the next turn.
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"Back at it."}]}}`)
	feed(`{"type":"result","duration_ms":1000}`)
	if end.Limited || !end.LimitAt.IsZero() {
		t.Errorf("stale limit: %+v", end)
	}
}

// How long a task waits on a usage limit, and when it stops waiting at all.
func TestLimitWait(t *testing.T) {
	p := testProject(map[string]*TaskInfo{"1": {Phase: PhaseAgent}})
	task := p.tasks["1"]
	now := time.Now()

	until, ok := task.limitWaitL(now.Add(2 * time.Hour))
	if !ok || until.Sub(now) < 2*time.Hour+limitSlack-time.Second {
		t.Errorf("a reset two hours out: %v %v", until, ok)
	}
	if _, ok := task.limitWaitL(time.Time{}); ok {
		t.Errorf("a limit naming no reset we can read is not one to wait out")
	}
	if until, ok := task.limitWaitL(now.Add(-time.Hour)); !ok || until.Before(now) {
		t.Errorf("a reset already behind us: %v %v", until, ok)
	}
	if _, ok := task.limitWaitL(now.Add(48 * time.Hour)); ok {
		t.Error("a reset two days out is not worth waiting for")
	}
	task.limitWaits = maxLimitWaits
	if _, ok := task.limitWaitL(now.Add(time.Hour)); ok {
		t.Error("a limit that keeps coming back is the user's")
	}
}

// A turn claude's usage limit cut short parks the task with the agent, keeps
// the machine awake for the wait, and gives the wait up when the task moves on.
func TestLimitPark(t *testing.T) {
	m := &Manager{projects: map[string]*Project{}, hub: hub.New(nil), saveCh: make(chan []byte, 1)}
	p := &Project{m: m, pid: "p", info: &ProjectInfo{Dir: t.TempDir(), Tasks: map[string]*TaskInfo{}}, tasks: map[string]*Task{}}
	m.projects["p"] = p
	info := &TaskInfo{Phase: PhaseAgent}
	task := newTask(p, "1", info)
	p.tasks["1"], p.info.Tasks["1"] = task, info

	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	task.onTurnEnd(TurnEnd{Failed: true, Limited: true, LimitAt: reset})
	if info.Phase != PhaseAgent || info.LimitUntil != reset.Add(limitSlack).UnixMilli() {
		t.Fatalf("not parked on the limit: %s, until %d", info.Phase, info.LimitUntil)
	}
	if !m.anyAwake() || m.anyWorking() {
		t.Error("the wait should hold the machine awake without counting as work")
	}
	if err := task.StopAgent(); err != nil {
		t.Fatal(err)
	}
	if info.Phase != PhaseHuman || info.LimitUntil != 0 || task.limitTimer != nil {
		t.Errorf("the wait outlived the stop: %s, until %d", info.Phase, info.LimitUntil)
	}

	// A verdict means the agent was talking about limits, not running into one.
	info.Phase = PhaseAgent
	task.onTurnEnd(TurnEnd{Failed: true, Limited: true, Text: "Summary: did it."})
	if info.Phase != PhaseHuman || info.LimitUntil != 0 {
		t.Errorf("a turn with a verdict was parked: %s, until %d", info.Phase, info.LimitUntil)
	}
}

// The message claude ends a turn with when its login no longer works, and the
// shapes it is not read into.
func TestAuthGone(t *testing.T) {
	yes := []string{
		"Failed to authenticate: OAuth session expired and could not be refreshed",
		"OAuth token has expired. Please obtain a new token.",
		"Invalid API key · Please run /login",
	}
	for _, text := range yes {
		if !authGone(text) {
			t.Errorf("authGone(%q) = false", text)
		}
	}
	for _, text := range []string{"", "I refreshed the session token in auth.go", "the tests authenticate against a stub"} {
		if authGone(text) {
			t.Errorf("authGone(%q) = true", text)
		}
	}

	var end TurnEnd
	s := &claudeSession{pending: map[string]*ChatEntry{}}
	s.agentProc = &agentProc{opts: SessionOpts{
		OnEntry:   func(*ChatEntry) {},
		OnUpdate:  func(*ChatEntry) {},
		OnTurnEnd: func(e TurnEnd) { end = e },
		OnExit:    func(int, string) {},
	}}
	feed := func(line string) {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		s.onEvent(&ev)
	}
	// Claude words it in a message, and labels the failed result 'success'.
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"Failed to authenticate: OAuth session expired and could not be refreshed"}]}}`)
	feed(`{"type":"result","subtype":"success","is_error":true,"duration_ms":300}`)
	if !end.NoLogin || !end.Failed {
		t.Fatalf("a turn that could not sign in: %+v", end)
	}
	// Or in the result itself — but only a failed one: a turn that went fine
	// carries the agent's own last words there, which may quote anything.
	feed(`{"type":"result","is_error":true,"result":"Failed to authenticate: OAuth session expired","duration_ms":300}`)
	if !end.NoLogin {
		t.Errorf("the reason was in the result: %+v", end)
	}
	feed(`{"type":"result","result":"Failed to authenticate: OAuth session expired","duration_ms":300}`)
	if end.NoLogin {
		t.Errorf("a turn that went fine was read for a login failure: %+v", end)
	}
}

// A turn that could not sign in asks the host for a sign-in, and hands the
// task over as any failed turn does.
func TestLoginFailed(t *testing.T) {
	m := &Manager{projects: map[string]*Project{}, hub: hub.New(nil), saveCh: make(chan []byte, 1)}
	p := &Project{m: m, pid: "p", info: &ProjectInfo{Dir: t.TempDir(), Tasks: map[string]*TaskInfo{}}, tasks: map[string]*Task{}}
	m.projects["p"] = p
	info := &TaskInfo{Phase: PhaseAgent}
	task := newTask(p, "1", info)
	p.tasks["1"], p.info.Tasks["1"] = task, info

	task.onTurnEnd(TurnEnd{Failed: true, NoLogin: true})
	if !m.loginGone.Load() {
		t.Error("the host was not asked for a sign-in")
	}
	if info.Phase != PhaseHuman {
		t.Errorf("the task did not go to the user: %s", info.Phase)
	}

	// A verdict means the agent was talking about logins, not failing on one.
	m.loginGone.Store(false)
	info.Phase = PhaseAgent
	task.onTurnEnd(TurnEnd{Failed: true, NoLogin: true, Text: "Summary: did it."})
	if m.loginGone.Load() {
		t.Error("a turn with a verdict asked for a sign-in")
	}
}

// Compaction: what claude says when it summarises away what it remembers, and
// the context figure that follows it back down.
func TestCompaction(t *testing.T) {
	var entries []*ChatEntry
	var ends []TurnEnd
	s := &claudeSession{pending: map[string]*ChatEntry{}}
	s.agentProc = &agentProc{opts: SessionOpts{
		OnEntry:   func(e *ChatEntry) { entries = append(entries, e) },
		OnUpdate:  func(*ChatEntry) {},
		OnTurnEnd: func(e TurnEnd) { ends = append(ends, e) },
		OnExit:    func(int, string) {},
	}}
	feed := func(line string) {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		s.onEvent(&ev)
	}
	// Everything the request sent counts as context: fresh, freshly cached and
	// read back from cache alike.
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello."}],"usage":{"input_tokens":12,"cache_creation_input_tokens":8,"cache_read_input_tokens":23000}}}`)
	feed(`{"type":"result","total_cost_usd":0.01,"duration_ms":1000,"is_error":false}`)
	if s.context != 23020 || len(ends) != 1 || ends[0].Context != 23020 {
		t.Fatalf("context not measured: %d, ends %+v", s.context, ends)
	}
	if entries[1].Text != "turn finished · 1s · $0.01 · 23k in context" {
		t.Errorf("result entry: %q", entries[1].Text)
	}

	feed(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":23117,"post_tokens":2734}}`)
	note := entries[2]
	if note.K != "note" || note.Text != "claude's context filled up, so it was compacted: 23k → 2.7k" || note.Detail == "" {
		t.Errorf("compaction note: %+v", note)
	}
	// What the last request sent is no longer what claude holds; the summary
	// is, until a request measures the window again.
	if s.context != 0 || s.summary != 2734 {
		t.Errorf("after a compaction: context %d, summary %d", s.context, s.summary)
	}
	feed(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":9000,"post_tokens":900}}`)
	if entries[3].Text != "claude compacted its context on request: 9k → 900" {
		t.Errorf("manual compaction note: %q", entries[3].Text)
	}
	// A compaction that failed is the one thing worse than one that happened:
	// claude carries on with a context it could not make room in.
	feed(`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"Not logged in · Please run /login"}`)
	if e := entries[4]; !e.Error || !strings.Contains(e.Text, "Not logged in") {
		t.Errorf("failed compaction: %+v", e)
	}
	if len(entries) != 5 {
		t.Errorf("got %d entries", len(entries))
	}
	// A turn that ends on a compaction says what it summarised the conversation
	// down to, and that the model was never called in it; one that is measured
	// again afterwards is measured, whatever it summarised.
	feed(`{"type":"result","total_cost_usd":0.02,"duration_ms":12000,"is_error":false,"num_turns":0}`)
	if end := ends[1]; end.Context != 0 || end.Summary != 900 || !end.Idle || entries[5].Text != "turn finished · 12s · $0.01" {
		t.Errorf("a compaction's turn: %+v, %q", end, entries[5].Text)
	}
	if s.summary != 0 {
		t.Error("the summary outlived the turn it was made in")
	}
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hi."}],"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":21000}}}`)
	feed(`{"type":"result","total_cost_usd":0.03,"duration_ms":1000,"is_error":false,"num_turns":1}`)
	if end := ends[2]; end.Context != 21010 || end.Idle {
		t.Errorf("a measured turn: %+v", end)
	}
	// A turn starts on its own, as one queued behind a compaction does.
	started := 0
	s.opts.OnTurnStart = func() { started++ }
	feed(`{"type":"system","subtype":"init","session_id":"s"}`)
	if started != 1 || !s.TurnActive() {
		t.Errorf("a turn starting was not heard of: %d, active %v", started, s.TurnActive())
	}
	for _, c := range []struct {
		n    int64
		want string
	}{{950, "950"}, {1234, "1.2k"}, {9000, "9k"}, {23117, "23k"}, {1_000_000, "1M"}, {1_250_000, "1.2M"}} {
		if got := tokens(c.n); got != c.want {
			t.Errorf("tokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// The context figure a task keeps is its agent's own; the reviewer's blank
// session says nothing about how full the conversation is.
func TestContextRecorded(t *testing.T) {
	m := &Manager{projects: map[string]*Project{}, hub: hub.New(nil), saveCh: make(chan []byte, 1)}
	p := &Project{m: m, pid: "p", info: &ProjectInfo{Dir: t.TempDir(), Tasks: map[string]*TaskInfo{}}, tasks: map[string]*Task{}}
	m.projects["p"] = p
	info := &TaskInfo{Phase: PhaseAgent}
	task := newTask(p, "1", info)
	p.tasks["1"], p.info.Tasks["1"] = task, info

	task.onTurnEnd(TurnEnd{Context: 34_000, Text: "Summary: did it."})
	if info.Context != 34_000 {
		t.Fatalf("the turn's context was not kept: %d", info.Context)
	}
	info.Phase, task.sessionReview = PhaseReview, true
	task.onTurnEnd(TurnEnd{Context: 900, Text: "Accept"})
	if info.Context != 34_000 {
		t.Errorf("the reviewer's context was taken for the task's: %d", info.Context)
	}
}

// A /compact is a turn of claude's session but not one of the task's: what it
// cost is counted, the window is drawn as holding the summary until a turn
// measures it, and nothing else is read into it — no verdict, no nudge for the
// line it never had, no handover — whoever the task was with.
func TestCompactTurn(t *testing.T) {
	m := &Manager{projects: map[string]*Project{}, hub: hub.New(nil), saveCh: make(chan []byte, 1)}
	p := &Project{m: m, pid: "p", info: &ProjectInfo{Dir: t.TempDir(), Tasks: map[string]*TaskInfo{}}, tasks: map[string]*Task{}}
	m.projects["p"] = p
	info := &TaskInfo{Phase: PhaseHuman, Context: 120_000, Window: &ContextWindow{Limit: 200_000, Parts: []ContextPart{{"System prompt", 15_000}, {"Tools", 5_000}}}}
	task := newTask(p, "1", info)
	p.tasks["1"], p.info.Tasks["1"] = task, info

	task.onTurnEnd(TurnEnd{Cost: 0.02, Summary: 4_000, Idle: true})
	if info.Spent != 0.02 || info.Context != 24_000 {
		t.Errorf("a compaction's cost and the summary on top of the fixed parts should both count: %+v", info)
	}
	if info.Phase != PhaseHuman || task.summaryNudges != 0 {
		t.Errorf("the task was moved along by a turn nothing was asked in: %s, %d nudges", info.Phase, task.summaryNudges)
	}
	// One queued behind a message of the user's ends while the task is the
	// agent's, and leaves it so: the message's turn is next.
	info.Phase = PhaseAgent
	task.onTurnEnd(TurnEnd{Idle: true})
	if info.Phase != PhaseAgent || task.summaryNudges != 0 {
		t.Errorf("the task was moved along by a turn nothing was asked in: %s, %d nudges", info.Phase, task.summaryNudges)
	}
	// A turn measured after it compacted is measured.
	task.onTurnEnd(TurnEnd{Context: 30_000, Summary: 4_000, Text: "Summary: did it."})
	if info.Context != 30_000 {
		t.Errorf("a measure was passed over for a summary: %d", info.Context)
	}
}

// A turn being stopped is the last one: a message queued during it, which the
// agent takes up the moment the turn ends, is not let through.
func TestStopDropsNextTurn(t *testing.T) {
	cmd := exec.Command("printf", `working\nend\nnext\nmore\n`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &agentProc{cmd: cmd, stdout: stdout, exited: make(chan struct{}), opts: SessionOpts{OnExit: func(int, string) {}}}
	p.turnActive.Store(true)
	p.stopping.Store(true)
	var lines []string
	p.run(func(line []byte) {
		lines = append(lines, string(line))
		p.turnActive.Store(string(line) != "end")
	})
	<-p.exited
	if strings.Join(lines, " ") != "working end" {
		t.Errorf("lines read: %q", lines)
	}
}
