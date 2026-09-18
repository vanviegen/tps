package daemon

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// What fills an agent's context window, and where it stops filling: the two
// things the dashboard's gauge needs, whichever agent the task runs (see
// Provider.Window). The types are shared; the asking below is claude's. Its
// `/context` is a local command — no API call, no cost — that prints a table
// of what its window holds and how much of it is left, so TPS asks for one and
// reads it. The asking is done by a throwaway claude of its own in the task's
// container, on the model the task's agent runs on: a second process cannot
// disturb the conversation the agent is having, where a command sent into that
// conversation would be a turn in it.
//
// What comes back is the same whatever the agent has been saying — the system
// prompt, the tools, the skills, and the room left for the rest — so one probe
// per model is enough, and the conversation's own share is the difference
// between it and what a turn reports sending (see onTurnEnd).

const probeScript = `
export PATH=/tps/bin:$PATH
exec /tps/bin/claude -p --output-format json --dangerously-skip-permissions \
	--no-session-persistence ${TPS_MODEL:+--model "$TPS_MODEL"} --append-system-prompt "$TPS_SYSTEM" /context
`

// probeTimeout: how long the probe may take before it is not worth waiting
// for. It answers in a second or two; a claude that needs longer than this is
// one with something else wrong with it.
const probeTimeout = 90 * time.Second

// ContextPart is one named share of an agent's context window: a category
// /context breaks it down into, or the conversation itself (see contextL).
type ContextPart struct {
	Name   string `json:"name"`
	Tokens int64  `json:"tokens"`
}

// ContextWindow is what one /context amounts to: where the conversation stops
// growing, and what is in the window besides it.
type ContextWindow struct {
	Model string        `json:"model"` // the model it was measured for; another model has another window
	Limit int64         `json:"limit"` // tokens the conversation may reach before the agent compacts it
	Parts []ContextPart `json:"parts"` // what is in there whatever has been said, in the agent's own order
}

// probeContext asks the claude in a container to break its context down.
func probeContext(c *Container, model, system string) (ContextWindow, bool) {
	// The default model is claude's own: pass no --model at all (see
	// claudeCLI.Start). Which model the window that comes back was measured for
	// is the caller's to record, in the words it asked in (see probeContextParts).
	if model == defaultModel {
		model = ""
	}
	r, err := runCmd([]string{"podman", "exec",
		"-e", "TPS_MODEL=" + model,
		"-e", "TPS_SYSTEM=" + system,
		c.Name, "bash", "-lc", probeScript}, RunOpts{Timeout: probeTimeout, NoCheck: true})
	if err != nil || r.Code != 0 {
		return ContextWindow{}, false
	}
	var out struct {
		Result string `json:"result"`
	}
	if json.Unmarshal([]byte(r.Out), &out) != nil {
		return ContextWindow{}, false
	}
	return parseContext(out.Result)
}

var (
	// "**Tokens:** 17.9k / 200k (9%)": the window's own size is the second
	// figure, which is the model's.
	contextTotal = regexp.MustCompile(`(?i)\*\*Tokens:\*\*[^/\n]*/\s*([0-9.]+[kKmM]?)`)
	// "| System prompt | 6.3k | 4.2% |"
	contextRow = regexp.MustCompile(`^\|\s*([^|]+?)\s*\|\s*([0-9.]+[kKmM]?)\s*\|`)
)

// parseContext reads the table /context prints. Only the first section is
// ours — the ones under it break a category down tool by tool, in a table of
// the same shape — and of its rows, the conversation's own and the two that
// are room rather than content are not parts. A category claude marks
// "(deferred)" is one it has left out of the window until something needs it,
// so it is not in there either.
func parseContext(md string) (ContextWindow, bool) {
	at := strings.Index(md, "### ")
	if at < 0 {
		return ContextWindow{}, false
	}
	section := md[at+4:]
	if end := strings.Index(section, "\n### "); end >= 0 {
		section = section[:end]
	}
	p := ContextWindow{}
	var buffer int64
	for _, line := range strings.Split(section, "\n") {
		m := contextRow.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		name, size := m[1], parseTokens(m[2])
		switch {
		case strings.EqualFold(name, "Category") || strings.HasPrefix(name, "---"):
		case strings.Contains(strings.ToLower(name), "(deferred)"):
		case strings.EqualFold(name, "Free space"):
		case strings.EqualFold(name, "Autocompact buffer"):
			buffer = size
		case strings.EqualFold(name, "Messages"): // the conversation's own share, which grows
		case size > 0:
			p.Parts = append(p.Parts, ContextPart{Name: name, Tokens: size})
		}
	}
	if m := contextTotal.FindStringSubmatch(md); m != nil {
		// Claude keeps the buffer free for the compaction itself, so the
		// conversation stops growing there rather than at the window's end.
		p.Limit = max(0, parseTokens(m[1])-buffer)
	}
	if p.Limit == 0 || len(p.Parts) == 0 {
		return ContextWindow{}, false // a table we could not read is no table
	}
	return p, true
}

// parseTokens reads a count as /context writes it: 8, 6.3k, 1.2M.
func parseTokens(s string) int64 {
	scale := 1.0
	switch {
	case strings.HasSuffix(strings.ToLower(s), "k"):
		scale, s = 1e3, s[:len(s)-1]
	case strings.HasSuffix(strings.ToLower(s), "m"):
		scale, s = 1e6, s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(n * scale)
}
