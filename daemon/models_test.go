package daemon

import (
	"slices"
	"strings"
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
