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

// Compacting is the agent's own memory: a review's session is nobody's
// conversation, so the task in review is not somewhere to ask for one.
func TestCompactRefusesInReview(t *testing.T) {
	task, _ := markTask(t) // a workspace, so the refusal is about the review and nothing else
	task.info.Started, task.info.Phase = true, PhaseReview

	if err := task.Compact(); err == nil {
		t.Error("a task in review should not compact: the session it would reach is the reviewer's")
	}
	if task.compacting {
		t.Error("a refused compaction still marked the turn as one")
	}
}
