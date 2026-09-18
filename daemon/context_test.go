package daemon

import "testing"

// What `claude -p /context` prints, as of 2.1.263.
const contextReport = "## Context Usage\n\n" +
	"**Model:** claude-haiku-4-5-20251001  \n" +
	"**Tokens:** 17.9k / 150k (12%)\n\n" +
	"### Estimated usage by category\n\n" +
	"| Category | Tokens | Percentage |\n" +
	"|----------|--------|------------|\n" +
	"| System prompt | 6.3k | 4.2% |\n" +
	"| System tools | 10.1k | 6.7% |\n" +
	"| System tools (deferred) | 15.4k | 10.3% |\n" +
	"| Skills | 1.5k | 1.0% |\n" +
	"| Messages | 8 | 0.0% |\n" +
	"| Free space | 99.1k | 66.0% |\n" +
	"| Autocompact buffer | 33k | 22.0% |\n\n" +
	"### Skills\n\n" +
	"| Skill | Source | Tokens |\n" +
	"|-------|--------|--------|\n" +
	"| dataviz | Built-in | ~360 |\n"

// Reading claude's own breakdown of what its window holds: the parts that are
// in it whatever is said, and the point it will compact at.
func TestParseContext(t *testing.T) {
	p, ok := parseContext(contextReport)
	if !ok {
		t.Fatal("the report claude prints should be readable")
	}
	// The window is 150k and claude keeps 33k of it free for the compaction
	// itself, so the conversation stops growing at 117k.
	if p.Limit != 117_000 {
		t.Errorf("limit %d, want 117000", p.Limit)
	}
	want := []ContextPart{{"System prompt", 6300}, {"System tools", 10100}, {"Skills", 1500}}
	if len(p.Parts) != len(want) {
		t.Fatalf("parts %+v, want %+v", p.Parts, want)
	}
	for i, part := range p.Parts {
		if part != want[i] {
			t.Errorf("part %d is %+v, want %+v", i, part, want[i])
		}
	}
	// The rows of the sections below the first are a table of the same shape,
	// breaking one category down; none of them is a part of its own.
	for _, part := range p.Parts {
		if part.Name == "dataviz" {
			t.Error("a skill was taken for a category of its own")
		}
	}
	if _, ok := parseContext("claude said something else entirely"); ok {
		t.Error("a report that is not one should not read as an empty one")
	}
}

func TestParseTokens(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want int64
	}{{"8", 8}, {"6.3k", 6300}, {"33k", 33_000}, {"1.2M", 1_200_000}, {"", 0}, {"lots", 0}} {
		if got := parseTokens(c.raw); got != c.want {
			t.Errorf("parseTokens(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

// The window as the dashboard gets it: the probed parts laid inside what the
// last turn really sent, and the conversation as whatever is left over.
func TestContextPublished(t *testing.T) {
	task := &Task{info: &TaskInfo{}}
	if task.contextL() != nil {
		t.Error("a task whose agent has not run has no window to draw")
	}
	task.info.Context = 21_000
	if task.contextL() != nil {
		t.Error("a turn measured without a window measured is half a picture")
	}
	task.info.Window = &ContextWindow{Limit: 117_000, Parts: []ContextPart{{"System prompt", 6300}, {"Skills", 1500}}}
	got, _ := task.contextL().(map[string]any)
	if got["used"] != int64(21_000) || got["limit"] != int64(117_000) {
		t.Fatalf("used and limit: %+v", got)
	}
	want := []ContextPart{{"System prompt", 6300}, {"Skills", 1500}, {conversationPart, 13_200}}
	parts, _ := got["parts"].([]ContextPart)
	if len(parts) != len(want) {
		t.Fatalf("parts %+v, want %+v", parts, want)
	}
	for i, p := range parts {
		if p != want[i] {
			t.Errorf("part %d is %+v, want %+v", i, p, want[i])
		}
	}
	// A window probed with more in it than the turn turned out to send — a
	// bigger tool set, say — must not fill more of the ring than was sent.
	task.info.Context = 5_000
	parts, _ = task.contextL().(map[string]any)["parts"].([]ContextPart)
	total := int64(0)
	for _, p := range parts {
		total += p.Tokens
	}
	if total != 5_000 || parts[len(parts)-1].Tokens != 0 {
		t.Errorf("the parts should stay inside what was sent: %+v", parts)
	}
}

// A window is recorded as measured for the model the task names, so a task on
// claude's own default is not re-probed at every session start. A container is
// what the probe would need, and there is none here: reaching for one is the
// bug this guards against.
func TestProbeSkipsMeasuredWindow(t *testing.T) {
	task, _ := testTask(t)
	task.info.Model = DefaultModel
	task.info.Window = &ContextWindow{Model: DefaultModel, Limit: 117_000}

	task.probeContextParts(nil)
	// The reviewer's window says nothing about the task's, whatever it holds.
	task.info.Window.Model = "opus"
	task.sessionReview = true
	task.probeContextParts(nil)
}

// A /compact goes to claude down the same road as a message, but not just
// anywhere a message may: not into a turn under way, which would drop it, and
// not to the reviewer, whose session is nobody's conversation.
func TestCompactRefusals(t *testing.T) {
	task, _ := markTask(t) // a workspace, so the refusals are about nothing else
	task.info.Started, task.info.Phase = true, PhaseHuman

	session := &claudeSession{agentProc: &agentProc{}}
	task.session = session
	session.turnActive.Store(true)
	if err := task.SendChat("/compact", nil); err == nil {
		t.Error("a /compact sent into a running turn is lost on it; it should be refused")
	}
	task.session = nil
	task.info.Phase = PhaseReview
	if err := task.SendChat("/compact keep the file list", nil); err == nil {
		t.Error("a task in review should not compact: the session it would reach is the reviewer's")
	}
	if task.info.Phase != PhaseReview {
		t.Errorf("a refused /compact moved the task to %s", task.info.Phase)
	}
	// Nor into a usage limit being waited out, which a message would call off.
	task.info.Phase, task.info.LimitUntil = PhaseAgent, 1
	if err := task.SendChat("/compact", nil); err == nil {
		t.Error("a /compact should not call off a wait for the limit it would run into")
	}
	if task.info.LimitUntil != 1 {
		t.Error("a refused /compact called the wait off")
	}
	// Nor a finished task, which a message would pick back up into a fresh clone.
	task.info.LimitUntil = 0
	if err := task.dropWorkspace(); err != nil {
		t.Fatal(err)
	}
	if err := task.SendChat("/compact", nil); err == nil {
		t.Error("a task without a workspace has nothing to compact in")
	}
}
