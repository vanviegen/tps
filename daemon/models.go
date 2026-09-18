package daemon

import (
	"strings"
)

// The models offered in a task's settings are every provider's, each named by
// the provider it belongs to: "claude: sonnet", "pi: z-ai/glm-5.3-flash". Each
// CLI is asked on this host for the models it takes, and answers for its own
// share of the list; "<provider>: default" leaves the choice to the CLI's own
// configuration, and leads that share.

// FallbackModels is what a task's settings offer until the CLIs have answered,
// and when asking them fails: each provider's own default.
var FallbackModels = fallbackModels()

func fallbackModels() []string {
	models := make([]string, len(providers))
	for i, p := range providers {
		models[i] = modelName(p, defaultModel)
	}
	return models
}

// refreshModels asks the CLIs this host has which models they offer and
// publishes them, next to why one of them could not be asked: a task settings
// dropdown that silently shows the fallback tells nobody what went wrong. The
// binaries live in the toolbox, so this waits for that download; a ticker
// keeps trying until every CLI has answered, and one that has is not asked
// again.
func (m *Manager) refreshModels() {
	m.modelsMu.Lock()
	defer m.modelsMu.Unlock()
	if len(m.models) == len(providers) {
		return
	}
	if !toolboxInstalled() {
		m.setModelsError("the agent CLIs have not been downloaded on this host yet")
		return
	}
	var models, failed []string
	for _, p := range providers {
		list, answered := m.models[p.Name()]
		if !answered {
			var err error
			if list, err = p.Models(); err != nil {
				failed = append(failed, p.Name()+" could not be asked which models it offers: "+err.Error())
				if !m.modelsFailed[p.Name()] { // once: the retries would fill the log
					m.modelsFailed[p.Name()] = true
					logf("Asking %s which models it offers failed (retrying): %v", p.Name(), err)
				}
				continue
			}
			m.models[p.Name()] = list
			logf("Models offered by %s: %s", p.Name(), strings.Join(list, ", "))
		}
		models = append(models, modelName(p, defaultModel))
		for _, name := range list {
			models = append(models, modelName(p, name))
		}
	}
	if len(models) > 0 { // nothing answered yet: the fallback list stays
		m.hub.Set([]string{"models"}, models)
	}
	m.setModelsError(strings.Join(failed, "; "))
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

// titleOf reads an agent's answer to titlePrompt: the title, or "" when it
// replied rather than named — its first 60 characters would say nothing.
func titleOf(out string) string {
	answer := strings.Trim(strings.TrimSpace(out), `"'`)
	if strings.Contains(answer, "\n") || len([]rune(answer)) > 80 {
		return ""
	}
	return head(answer, 60)
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
