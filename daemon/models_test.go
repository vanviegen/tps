package daemon

import (
	"slices"
	"strings"
	"testing"
)

func TestParseModels(t *testing.T) {
	out := "Current model: `Fable 5.1` (effort: xhigh)\n" +
		"Usage: /model <name>. Available: sonnet, opus, haiku, fable, best, sonnet[1m], opus[1m], fable[1m], opusplan, default, or a full model ID.\n"
	want := []string{"sonnet", "opus", "haiku", "fable", "best", "sonnet[1m]", "opus[1m]", "fable[1m]", "opusplan"}
	if got := parseModels(out); !slices.Equal(got, want) {
		t.Errorf("parseModels: %q", got)
	}
	for _, bad := range []string{"", "Current model: `Fable 5.1`\n", "Available: or a full model ID.\n"} {
		if got := parseModels(bad); got != nil {
			t.Errorf("parseModels(%q): %q, want nil", bad, got)
		}
	}
}

func TestParsePiModels(t *testing.T) {
	out := "provider    model                context  max-out  thinking  images\n" +
		"openrouter  z-ai/glm-5.3-flash   1.0M     943.7K   yes       yes\n" +
		"openrouter  anthropic/claude-sonnet-5  1M  128K     yes       yes\n" +
		"anthropic   anthropic/claude-sonnet-5  1M  128K     yes       yes\n"
	want := []string{"z-ai/glm-5.3-flash", "anthropic/claude-sonnet-5"}
	if got := parsePiModels(out); !slices.Equal(got, want) {
		t.Errorf("parsePiModels: %q", got)
	}
	if got := parsePiModels("No models available. Use /login to log into a provider.\n"); got != nil {
		t.Errorf("parsePiModels without models: %q", got)
	}
}

// A model names the agent to run it on, and a name from before there was more
// than one still names claude.
func TestSplitModel(t *testing.T) {
	cases := map[string][2]string{
		"claude: sonnet":            {"claude", "sonnet"},
		"pi: z-ai/glm-5.3-flash":    {"pi", "z-ai/glm-5.3-flash"},
		"sonnet":                    {"claude", "sonnet"},
		"anthropic/claude-sonnet-5": {"claude", "anthropic/claude-sonnet-5"},
	}
	for name, want := range cases {
		p, model := splitModel(name)
		if p.Name() != want[0] || model != want[1] {
			t.Errorf("splitModel(%q): %s, %q", name, p.Name(), model)
		}
	}
	if modelName(piCLI{}, defaultModel) != "pi: default" || DefaultModel != "claude: default" {
		t.Errorf("model names: %q, %q", modelName(piCLI{}, defaultModel), DefaultModel)
	}
}

func TestHead(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Add a dark mode toggle\n", "Add a dark mode toggle"},
		{"Fix the parser\nand then some", "Fix the parser"},
		{"", ""},
		{"\n\n", ""},
		{strings.Repeat("é", 80), strings.Repeat("é", 60) + "…"},
	}
	for _, c := range cases {
		if got := head(c.in, 60); got != c.want {
			t.Errorf("head(%q): %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDraftTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		// The heading marks and space a description often opens with are not a title.
		{"# Remove left-column selectors\n\nInstead, show breadcrumbs.", "Remove left-column select…"},
		{"   ## Fix it", "Fix it"},
		{"Short one", "Short one"},
		{"", ""},
	}
	for _, c := range cases {
		if got := draftTitle(c.in); got != c.want {
			t.Errorf("draftTitle(%q): %q, want %q", c.in, got, c.want)
		}
	}
}
