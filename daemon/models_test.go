package daemon

import (
	"slices"
	"testing"
)

func TestParseModels(t *testing.T) {
	out := "Current model: `Fable 5.1` (effort: xhigh)\n" +
		"Usage: /model <name>. Available: sonnet, opus, haiku, fable, best, sonnet[1m], opus[1m], fable[1m], opusplan, default, or a full model ID.\n"
	want := []string{"default", "sonnet", "opus", "haiku", "fable", "best", "sonnet[1m]", "opus[1m]", "fable[1m]", "opusplan"}
	if got := parseModels(out); !slices.Equal(got, want) {
		t.Errorf("parseModels: %q", got)
	}
	for _, bad := range []string{"", "Current model: `Fable 5.1`\n", "Available: or a full model ID.\n"} {
		if got := parseModels(bad); got != nil {
			t.Errorf("parseModels(%q): %q, want nil", bad, got)
		}
	}
}
