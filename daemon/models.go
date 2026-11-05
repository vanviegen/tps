package daemon

import (
	"strings"
)

// The models offered in a task's settings are the ones the claude CLI on this
// host says it takes (see claudeModels). "default" leaves the choice to
// claude's own configuration, leads the list, and is what a new task gets.

// DefaultModel is the model a new task starts with: claude's own default.
const DefaultModel = "default"

// FallbackModels is what a task's settings offer until claude has answered,
// and when asking it fails.
var FallbackModels = []string{DefaultModel}

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
	models, err := claudeModels()
	if err != nil {
		m.setModelsError(err.Error())
		if !m.modelsFailed { // once: the retries would fill the log
			m.modelsFailed = true
			logf("Asking claude which models it offers failed (retrying): %v", err)
		}
		return
	}
	m.modelsFound = true
	logf("Models offered by claude: %s", strings.Join(models, ", "))
	m.setModelsError("")
	m.hub.Set([]string{"models"}, append([]string{DefaultModel}, models...))
}

// bareModel reads a model setting written by a TPS that named the agent CLI
// in it, "claude: sonnet", as the model alone; one of another CLI's is
// claude's default.
func bareModel(name string) string {
	if cli, model, ok := strings.Cut(name, ": "); ok {
		if cli != "claude" {
			return DefaultModel
		}
		return model
	}
	return name
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

// draftTitle is what a task is called until the agent has thought about it:
// the head of its description, with markdown heading marks and space stripped.
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
