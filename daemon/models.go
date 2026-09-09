package daemon

import (
	"errors"
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
// them. The claude binary lives in the toolbox, so this is a no-op until that
// has been downloaded; it is retried until it yields an answer.
func (m *Manager) refreshModels() {
	m.modelsMu.Lock()
	defer m.modelsMu.Unlock()
	if m.modelsFound || !toolboxInstalled() {
		return
	}
	r, err := runCmd([]string{claudeBin(), "-p", "/model"}, RunOpts{Dir: home(), Timeout: 60 * time.Second})
	models := parseModels(r.Out)
	if models == nil {
		if err == nil {
			err = errors.New("no model list in its output")
		}
		logf("Asking claude which models it offers failed: %v", err)
		return
	}
	m.modelsFound = true
	logf("Models offered by claude: %s", strings.Join(models, ", "))
	m.hub.Set([]string{"models"}, models)
}

// generateTitle asks this host's claude for a task title: haiku, as naming one
// is little work, and a one-shot -p run, as there is nothing to discuss. It
// answers "" when it can't be asked (no toolbox yet, no network) or won't say
// anything usable; the stand-in title then simply stays.
func generateTitle(description string) string {
	r, err := runCmd([]string{claudeBin(), "-p", titlePrompt(description), "--model", "haiku"}, RunOpts{Dir: home(), Timeout: 30 * time.Second})
	if err != nil {
		logf("asking claude for a task title failed: %v", err)
		return ""
	}
	return head(strings.Trim(strings.TrimSpace(r.Out), `"'`), 60)
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
