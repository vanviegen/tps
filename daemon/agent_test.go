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
	var costs []float64
	s := &ChatSession{pending: map[string]*ChatEntry{}, opts: SessionOpts{
		OnEntry:   func(e *ChatEntry) { entries = append(entries, e) },
		OnUpdate:  func(e *ChatEntry) { updates = append(updates, e) },
		OnTurnEnd: func(d float64) { costs = append(costs, d) },
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
	if entries[2].K != "tool" || entries[2].Arg != "/work/a.go: x → y" || entries[2].Text != "Fix a" || !strings.Contains(entries[2].Detail, "file_path: /work/a.go") {
		t.Errorf("tool entry: %+v", entries[2])
	}
	if len(updates) != 2 || updates[0] != entries[2] || updates[0].Res != "ok done" || updates[0].ResDetail != "ok\ndone" || updates[0].Error {
		t.Errorf("tool result merge: %+v", updates)
	}
	if updates[1].Res != "(no output)" || !updates[1].Error || updates[1].Arg != "ls" {
		t.Errorf("bash result: %+v", updates[1])
	}
	if entries[4].Text != "turn finished · 3s · $0.02" || len(costs) != 2 || costs[0] != 0.0234 || costs[1] < 0.0065 || costs[1] > 0.0067 {
		t.Errorf("result entry: %q costs %v", entries[4].Text, costs)
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

func TestContainerfile(t *testing.T) {
	ids := detectTools([]string{"go.mod", "Cargo.toml", "README.md", "yarn.lock"})
	if strings.Join(ids, ",") != "corepack,go,rust" {
		t.Errorf("detected %v", ids)
	}
	cf := generateContainerfile([]string{"rust", "python"})
	py, rs := strings.Index(cf, "# --- Python"), strings.Index(cf, "# --- Rust")
	if py < 0 || rs < 0 || py > rs || !strings.Contains(cf, "# --- End of TPS base ---") || !strings.HasSuffix(cf, "USER dev\nWORKDIR /work\n") {
		t.Errorf("containerfile:\n%s", cf)
	}
	if imageTag(cf) != imageTag(cf) || len(imageTag(cf)) != len("localhost/tps:")+12 {
		t.Error("image tag")
	}
	if Slugify("My Project!!") != "my-project" || Slugify("--") != "x" {
		t.Error("slugify")
	}
}
