package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// One claude turn, from tool call to result, condensed into chat entries.
func TestSessionEvents(t *testing.T) {
	var entries []*ChatEntry
	var updates []*ChatEntry
	var ends []TurnEnd
	s := &ChatSession{pending: map[string]*ChatEntry{}, opts: SessionOpts{
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
	if ends[0].Done != nil || ends[0].Bad != "" || ends[0].Failed || !ends[1].Failed {
		t.Errorf("verdictless turn: %+v", ends[0])
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

// The TPS-DONE line: stripped from the message, and carried to the turn's end.
func TestDoneLine(t *testing.T) {
	cases := []struct {
		in, rest, next, msg, bad string
	}{
		{in: "All set.\n\nTPS-DONE: {\"next\": \"user\"}", rest: "All set.", next: "user"},
		{in: "TPS-DONE: {\"next\":\"merge\",\"message\":\"Sum\\n\\nDetail\"}", next: "merge", msg: "Sum\n\nDetail"},
		{in: "Rebuilding.\nTPS-DONE: {\n  \"next\": \"reload\"\n}", rest: "Rebuilding.", next: "reload"},
		{in: "a\nTPS-DONE: {\"next\": \"user\"}\nb", rest: "a", bad: "the JSON after it could not be read"}, // not the end
		{in: "```\nTPS-DONE: {\"next\": \"user\"}\n```", next: "user"},
		{in: "TPS-DONE: {\"next\": \"user\"}\nTPS-DONE: {\"next\": \"merge\"}", rest: "TPS-DONE: {\"next\": \"user\"}", next: "merge"}, // the last one counts
		{in: "Nothing to see here.", rest: "Nothing to see here."},
		{in: "x\nTPS-DONE: {\"next\": \"done\"}", rest: "x", bad: "'done' is not one of user, merge, reload"},
		{in: "x\nTPS-DONE: {}", rest: "x", bad: "it has no 'next'"},
		{in: "x\nTPS-DONE: merge please", rest: "x", bad: "the JSON after it could not be read"},
	}
	for _, c := range cases {
		rest, done, bad := parseDone(c.in)
		next, msg := "", ""
		if done != nil {
			next, msg = done.Next, done.Message
		}
		if rest != c.rest || next != c.next || msg != c.msg || bad != c.bad {
			t.Errorf("parseDone(%q) = %q, %q/%q, %q", c.in, rest, next, msg, bad)
		}
	}

	var entries []*ChatEntry
	var end TurnEnd
	s := &ChatSession{pending: map[string]*ChatEntry{}, opts: SessionOpts{
		OnEntry:   func(e *ChatEntry) { entries = append(entries, e) },
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
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"Done.\n\nTPS-DONE: {\"next\": \"merge\", \"message\": \"Do a thing\"}"}]}}`)
	feed(`{"type":"result","total_cost_usd":0.01,"duration_ms":1000}`)
	if len(entries) != 2 || entries[0].Text != "Done." {
		t.Fatalf("entries: %+v", entries)
	}
	if end.Done == nil || end.Done.Next != "merge" || end.Done.Message != "Do a thing" {
		t.Fatalf("verdict: %+v", end)
	}
	// A later message without a line of its own drops the earlier verdict, and
	// no verdict carries over into the next turn.
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"TPS-DONE: {\"next\": \"user\"}"}]}}`)
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"Actually, one more thing."}]}}`)
	feed(`{"type":"result","total_cost_usd":0.02,"duration_ms":1000}`)
	if end.Done != nil || end.Bad != "" {
		t.Errorf("stale verdict: %+v", end)
	}
}
