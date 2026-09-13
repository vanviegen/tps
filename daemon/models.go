package daemon

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The models offered in a task's settings are the ones the claude CLI on this
// host says it takes: `claude -p /model` is a local slash command (no API
// call) that prints them. "default" leaves the choice to claude's own
// configuration, and is what a new task gets.

// DefaultModel is the model a new task starts with: claude's own default.
const DefaultModel = "default"

// FallbackModels is what a task's settings offer until claude has answered,
// and when asking it fails.
var FallbackModels = []string{DefaultModel, "sonnet", "opus", "haiku"}

var availableRe = regexp.MustCompile(`\bAvailable:[ \t]*([^\n]+)`)
var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:\[\]/-]*$`)

// parseModels picks the model names out of `claude -p /model` output:
//
//	Usage: /model <name>. Available: sonnet, opus, …, default, or a full model ID.
//
// Whatever is not a bare name (the trailing "or a full model ID") is dropped,
// and "default" leads the list.
func parseModels(out string) []string {
	m := availableRe.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	models := []string{DefaultModel}
	seen := map[string]bool{DefaultModel: true}
	for _, part := range strings.Split(m[1], ",") {
		name := strings.Trim(strings.TrimSpace(part), ".")
		if !modelRe.MatchString(name) || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, name)
	}
	if len(models) == 1 {
		return nil
	}
	return models
}

// refreshModels asks this host's claude which models it offers and publishes
// them, next to why it could not: a task settings dropdown that silently shows
// the fallback tells nobody what went wrong. The claude binary lives in the
// toolbox, so this waits for that download; a ticker keeps trying until claude
// answers.
func (m *Manager) refreshModels() {
	m.modelsMu.Lock()
	defer m.modelsMu.Unlock()
	if m.modelsFound {
		return
	}
	if !toolboxInstalled() {
		m.setModelsError("claude has not been downloaded on this host yet")
		return
	}
	r, err := runCmd(askArgs("/model"), RunOpts{Dir: home(), Env: askEnv(), Timeout: 60 * time.Second})
	models := parseModels(r.Out + "\n" + r.Err)
	if models == nil {
		if err == nil {
			err = fmt.Errorf("no model list in `%s`", firstLine(r.Out+r.Err))
		}
		m.setModelsError(err.Error())
		if !m.modelsFailed { // once: the retries would fill the log
			m.modelsFailed = true
			logf("Asking claude which models it offers failed (retrying); until it answers, tasks can pick %s: %v",
				strings.Join(FallbackModels, ", "), err)
		}
		return
	}
	m.modelsFound = true
	logf("Models offered by claude: %s", strings.Join(models, ", "))
	m.hub.Set([]string{"modelsError"}, "")
	m.hub.Set([]string{"models"}, models)
}

func (m *Manager) setModelsError(msg string) {
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	m.hub.Set([]string{"modelsError"}, msg)
}

// firstLine of a command's output, for an error message.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}

// askArgs: a one-shot errand for this host's claude. Naming a task and asking
// which models there are is no conversation, so everything claude sets up for a
// session it keeps is only in the way. The MCP servers a user has configured
// are the expensive part — connecting to them takes seconds before the first
// token is asked for — and a session nothing will resume needs no saving.
func askArgs(args ...string) []string {
	argv := append([]string{claudeBin(), "-p"}, args...)
	return append(argv, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence")
}

// askEnv gives the run the daemon's claude dir (see login.go) and drops the two model
// calls it would make on the side: claude having a model name the session
// behind our back, and thinking. The answers wanted here are one line long,
// and claude spent ten times as many tokens thinking about a title as it did
// saying it. All together, a title now takes about a second, where the full
// session took seven to ten.
func askEnv() []string {
	return append(claudeEnv(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "MAX_THINKING_TOKENS=0")
}

// generateTitle asks this host's claude for a task title: haiku, as naming one
// is little work, and a one-shot -p run, as there is nothing to discuss. It
// answers "" when it can't be asked (no toolbox yet, no network) or won't say
// anything usable; the stand-in title then simply stays.
func generateTitle(description string) string {
	r, err := runCmd(askArgs(titlePrompt(description), "--model", "haiku"), RunOpts{Dir: home(), Env: askEnv(), Timeout: 30 * time.Second})
	if err != nil {
		logf("asking claude for a task title failed: %v", err)
		return ""
	}
	answer := strings.Trim(strings.TrimSpace(r.Out), `"'`)
	if strings.Contains(answer, "\n") || len([]rune(answer)) > 80 {
		return "" // a reply, not a title: its first 60 characters would say nothing
	}
	return head(answer, 60)
}

// draftTitle is what a task is called until claude has thought about it: the
// head of its description, with markdown heading marks and space stripped.
func draftTitle(description string) string {
	return head(description, 25)
}

// head: the first line of text, at most max characters, saying where it cut.
func head(text string, max int) string {
	line, _, _ := strings.Cut(strings.TrimLeft(strings.TrimSpace(text), "# \t"), "\n")
	line = strings.TrimSpace(line)
	if runes := []rune(line); len(runes) > max {
		line = strings.TrimSpace(string(runes[:max])) + "…"
	}
	return line
}
