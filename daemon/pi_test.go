package daemon

import (
	"strings"
	"testing"
)

// stdinLog stands in for the process's stdin: what the session asks pi for.
type stdinLog struct{ lines []string }

func (w *stdinLog) Write(p []byte) (int, error) {
	w.lines = append(w.lines, string(p))
	return len(p), nil
}
func (w *stdinLog) Close() error { return nil }

// One pi turn, from tool call to the stats it ends on, condensed into chat entries.
func TestPiSessionEvents(t *testing.T) {
	var entries []*ChatEntry
	var updates []*ChatEntry
	var ends []TurnEnd
	stdin := &stdinLog{}
	s := &piSession{pending: map[string]*ChatEntry{}}
	s.agentProc = &agentProc{stdin: stdin, opts: SessionOpts{
		OnEntry:     func(e *ChatEntry) { entries = append(entries, e) },
		OnUpdate:    func(e *ChatEntry) { updates = append(updates, e) },
		OnTurnStart: func() {},
		OnTurnEnd:   func(e TurnEnd) { ends = append(ends, e) },
		OnExit:      func(int, string) {},
	}}
	feed := func(line string) { s.onLine([]byte(line)) }

	// What the session had cost before this process picked it up (see Start).
	feed(`{"type":"response","command":"get_session_stats","success":true,"data":{"cost":0.01}}`)
	s.Send("get to work")
	feed(`{"type":"agent_start"}`)
	feed(`{"type":"message_end","message":{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"  let me   think\nabout it "},{"type":"text","text":"Working on it."},` +
		`{"type":"toolCall","id":"call_1","name":"edit","arguments":{"path":"/work/a.go","edits":[{"oldText":"x","newText":"y"}]}}],"stopReason":"toolUse"}}`)
	feed(`{"type":"tool_execution_end","toolCallId":"call_1","toolName":"edit","result":{"content":[{"type":"text","text":"ok\ndone"}]},"isError":false}`)
	feed(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Done.\n\nTPS-DONE: {\"next\": \"user\", \"changes\": \"a.go\"}"}],"stopReason":"stop"}}`)
	feed(`{"type":"agent_settled"}`)
	feed(`{"type":"response","command":"get_session_stats","success":true,"data":{"cost":0.0234,"contextUsage":{"tokens":1520,"contextWindow":128000}}}`)

	if !strings.Contains(stdin.lines[0], `"streamingBehavior":"steer"`) || !strings.Contains(stdin.lines[1], "get_session_stats") {
		t.Fatalf("what the session asked pi for: %q", stdin.lines)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	if entries[0].K != "thinking" || entries[0].Text != "let me think about it" {
		t.Errorf("thinking entry: %+v", entries[0])
	}
	if entries[2].K != "tool" || entries[2].Arg != "/work/a.go: x → y" || len(entries[2].Req) != 2 {
		t.Errorf("tool entry: %+v", entries[2])
	}
	if len(updates) != 1 || updates[0] != entries[2] || updates[0].Res != "ok done" || updates[0].Error {
		t.Errorf("tool result merge: %+v", updates)
	}
	if entries[3].Text != "Done." {
		t.Errorf("the text entry keeps the TPS-DONE line out of the log: %q", entries[3].Text)
	}
	if !strings.HasPrefix(entries[4].Text, "turn finished · ") || !strings.HasSuffix(entries[4].Text, "$0.01 · 1.5k in context") {
		t.Errorf("result entry: %q", entries[4].Text)
	}
	if len(ends) != 1 || ends[0].Cost != 0.0134 || ends[0].Context != 1520 || ends[0].Done == nil || ends[0].Done.Changes != "a.go" {
		t.Errorf("turn end: %+v", ends)
	}
	if s.TurnActive() {
		t.Error("turn still active after the stats came back")
	}

	// A request that failed for good fails the turn, and says why.
	feed(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Connection error."}}`)
	feed(`{"type":"agent_settled"}`)
	feed(`{"type":"response","command":"get_session_stats","success":true,"data":{"cost":0.0234}}`)
	if n := len(entries); entries[n-2].Text != "Connection error." || !entries[n-2].Error || !strings.HasPrefix(entries[n-1].Text, "turn failed") {
		t.Errorf("failed turn: %+v", entries[len(entries)-2:])
	}
	if len(ends) != 2 || !ends[1].Failed || ends[1].Cost != 0 {
		t.Errorf("failed turn end: %+v", ends[1])
	}
}

// A /compact is pi's own command, and the turn it makes is one the agent was
// asked nothing in.
func TestPiCompact(t *testing.T) {
	var entries []*ChatEntry
	var ends []TurnEnd
	stdin := &stdinLog{}
	s := &piSession{pending: map[string]*ChatEntry{}}
	s.agentProc = &agentProc{stdin: stdin, opts: SessionOpts{
		OnEntry:   func(e *ChatEntry) { entries = append(entries, e) },
		OnTurnEnd: func(e TurnEnd) { ends = append(ends, e) },
	}}
	s.onLine([]byte(`{"type":"response","command":"get_session_stats","success":true,"data":{"cost":0}}`))
	s.Send("/compact keep the file list")
	if !strings.Contains(stdin.lines[0], `"type":"compact"`) || !strings.Contains(stdin.lines[0], "keep the file list") {
		t.Fatalf("the compaction pi was asked for: %q", stdin.lines[0])
	}
	s.onLine([]byte(`{"type":"compaction_end","reason":"manual","result":{"tokensBefore":150000,"estimatedTokensAfter":32000}}`))
	s.onLine([]byte(`{"type":"response","command":"compact","success":true}`))
	s.onLine([]byte(`{"type":"response","command":"get_session_stats","success":true,"data":{"cost":0.03}}`))
	if entries[0].Text != "the agent compacted its context on request: 150k → 32k" || entries[0].Detail == "" {
		t.Errorf("compaction note: %+v", entries[0])
	}
	if len(ends) != 1 || !ends[0].Idle || ends[0].Summary != 32000 || ends[0].Cost != 0.03 {
		t.Errorf("compaction turn: %+v", ends)
	}
}
