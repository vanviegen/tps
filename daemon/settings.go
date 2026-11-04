package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Agents help set projects up, so they read the project's settings with
// 'tps-guest-tool settings', and suggest changes with 'tps-guest-tool settings
// suggest': a suggestion waits in the task for the user to accept or reject,
// the tool waiting with it. The tool and the daemon talk through files in the
// task's services dir: the daemon keeps settingsFile current (see
// syncSettingsL), the tool leaves its suggestion in askDir, and the daemon
// answers beside it. A suggestion answered after the tool gave up waiting is
// told the agent the next time it is sent in.
const (
	settingsFile   = ".settings.json"
	suggestionFile = "settings.suggestion" // in askDir, which a container that goes takes along (see resetServices)
	suggestedFile  = "settings.answer"
)

// settingsL are the project's settings, as SetConfig takes them.
func (p *Project) settingsL() map[string]any {
	return map[string]any{
		"name":       p.info.Name,
		"color":      p.info.Color,
		"autoDeploy": p.info.AutoDeploy,
		"mounts":     append([]Mount{}, p.info.Mounts...),
		"seeds":      append([]string{}, p.info.Seeds...),
		"defaults":   p.defaultsL(),
	}
}

// checkSettings checks a change to a project's settings — the dashboard's,
// or one an agent suggests — and tidies it into what SetConfig puts in
// place: a field it does not know, or a value it would not take, is an error.
func checkSettings(partial map[string]any) (map[string]any, error) {
	tidy := map[string]any{}
	for key, v := range partial {
		switch key {
		case "name":
			name, _ := v.(string)
			if name = strings.TrimSpace(name); name == "" {
				return nil, errors.New("Give the project a name")
			}
			v = name
		case "color":
			color, _ := v.(string)
			if !colorRe.MatchString(color) {
				return nil, errors.New("A colour is #rrggbb")
			}
			v = strings.ToLower(color)
		case "autoDeploy":
			if _, ok := v.(bool); !ok {
				return nil, errors.New("autoDeploy is true or false")
			}
		case "mounts":
			raw, ok := v.([]any)
			mounts := []Mount{}
			for _, r := range raw {
				m, isMap := r.(map[string]any)
				ok = ok && isMap
				path, _ := m["path"].(string)
				write, _ := m["write"].(bool)
				all, _ := m["allTasks"].(bool)
				if path = strings.TrimSpace(path); path != "" {
					mounts = append(mounts, Mount{path, write, all})
				}
			}
			if !ok {
				return nil, errors.New("The mounts are a list of objects")
			}
			v = mounts
		case "seeds":
			raw, ok := v.([]any)
			if !ok {
				return nil, errors.New("The seeds are a list of paths")
			}
			seeds := []string{}
			for _, r := range raw {
				path, _ := r.(string)
				if path = strings.TrimSpace(path); path == "" {
					continue
				}
				path = filepath.Clean(path)
				if !filepath.IsLocal(path) || path == ".git" || strings.HasPrefix(path, ".git/") {
					return nil, fmt.Errorf("A seed is a path in the project's checkout, outside .git: %q is not", path)
				}
				seeds = append(seeds, path)
			}
			v = seeds
		case "defaults":
			defaults, ok := v.(map[string]any)
			if !ok {
				return nil, errors.New("The defaults are an object")
			}
			for key, v := range defaults {
				if err := checkDefault(key, v); err != nil {
					return nil, fmt.Errorf("defaults.%s: %w", key, err)
				}
			}
		default:
			return nil, fmt.Errorf("There is no setting %q", key)
		}
		tidy[key] = v
	}
	return tidy, nil
}

// checkDefault checks a default task setting, as SetConfig takes it.
func checkDefault(key string, v any) error {
	ok := true
	switch key {
	case "model":
		model, _ := v.(string)
		ok = model != ""
	case "reviewModel":
		_, ok = v.(string)
	case "onReady":
		_, ok = parseAnswer(v, AnswerHuman, AnswerReview, AnswerMerge)
	case "onAccept":
		_, ok = parseAnswer(v, AnswerHuman, AnswerMerge)
	case "reviewLoops":
		_, ok = parseLoops(v)
	case "budget", "openrouterBudget": // none, or a number, or one typed in
		switch v.(type) {
		case nil, float64, string:
		default:
			ok = false
		}
	default:
		return errors.New("no such setting")
	}
	if !ok {
		return fmt.Errorf("%v is not a value it takes", v)
	}
	return nil
}

// seed copies the project's seeds — what its checkout has installed or built
// that git ignores — into a workspace being made, so that it starts with
// them: as reflinks where the filesystem has them, which cost next to
// nothing. A seed that is not there, or that git tracks, is left out; so is
// one reached through a symlink out of the checkout.
func (t *Task) seed(repo string) {
	t.lock()
	seeds, checkout := t.p.info.Seeds, t.p.dir()
	t.unlock()
	for _, path := range seeds {
		from, to := filepath.Join(checkout, path), filepath.Join(repo, path)
		parent, err := filepath.EvalSymlinks(filepath.Dir(from))
		if err != nil || !exists(from) || exists(to) || !gitOK(checkout, "check-ignore", "-q", path) {
			continue
		}
		if real, _ := filepath.EvalSymlinks(checkout); parent != real && !strings.HasPrefix(parent, real+"/") {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(to), 0o755)
		if _, err := runCmd([]string{"cp", "-a", "--reflink=auto", from, to}, RunOpts{}); err != nil {
			_ = os.RemoveAll(to)
			t.noteErr("Seeding the workspace with "+path+" failed", err)
		}
	}
}

// --- the tool's side ---

func (st *guestTool) settings(args []string) int {
	switch {
	case len(args) == 0:
		data, err := os.ReadFile(filepath.Join(st.root, settingsFile))
		if err != nil {
			return st.fail(errors.New("settings: TPS has not put them in this container yet; try again in a few seconds"))
		}
		os.Stdout.Write(data)
		return 0
	case len(args) == 2 && args[0] == "suggest":
		return st.suggest(args[1])
	}
	return st.fail(errors.New("settings: run as 'settings', or as 'settings suggest <json>'"))
}

// suggest puts a change to the user, and waits for their answer as long as an
// OpenRouter request does.
func (st *guestTool) suggest(raw string) int {
	var partial map[string]any
	err := json.Unmarshal([]byte(raw), &partial)
	if err == nil {
		partial, err = checkSettings(partial)
	}
	if err != nil {
		return st.fail(fmt.Errorf("settings suggest: %w", err))
	}
	dir := filepath.Join(st.root, askDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st.fail(err)
	}
	file, answer := filepath.Join(dir, suggestionFile), filepath.Join(dir, suggestedFile)
	_ = os.Remove(answer)
	data, _ := json.Marshal(partial)
	if err := writeAtomic(file, data); err != nil {
		return st.fail(err)
	}
	// Not waiting any more, the suggestion is left standing for the user, and
	// its answer for the agent's next turn.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigs
		os.Remove(file)
		os.Exit(130)
	}()
	for deadline := time.Now().Add(orWait); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		said := readFile(answer)
		if said == "" {
			continue
		}
		os.Remove(answer)
		os.Remove(file)
		switch said {
		case "accepted":
			fmt.Println("The user accepted the suggestion.")
			return 0
		case "rejected":
			fmt.Println("The user rejected the suggestion.")
			return 1
		}
		return st.fail(errors.New("settings suggest: nobody is there to answer: this is a scheduled run"))
	}
	os.Remove(file)
	fmt.Fprintln(os.Stderr, "The user has not answered in half an hour. The suggestion stands in the dashboard, and you hear their answer the next time you are sent in.")
	return 124
}

// --- the daemon's side ---

// syncSettingsL keeps the settings the tool reads current, and takes up a
// suggestion it left. A scheduled run's is turned down: nobody is there to
// weigh it.
func (t *Task) syncSettingsL() {
	if data, _ := json.MarshalIndent(t.p.settingsL(), "", "  "); string(data) != t.settingsShown {
		if writeAtomic(filepath.Join(t.servicesDir(), settingsFile), append(data, '\n')) == nil {
			t.settingsShown = string(data)
		}
	}
	dir := filepath.Join(t.servicesDir(), askDir)
	raw, err := os.ReadFile(filepath.Join(dir, suggestionFile))
	if err != nil || exists(filepath.Join(dir, suggestedFile)) {
		return
	}
	if taken, _ := json.Marshal(t.info.Suggestion); t.info.Suggestion != nil && string(taken) == string(raw) {
		return
	}
	// Kept as it was read, which is what SetConfig takes.
	var partial map[string]any
	err = json.Unmarshal(raw, &partial)
	if err == nil {
		_, err = checkSettings(partial)
	}
	switch {
	case err != nil:
		t.noteErr("The agent's suggested project settings could not be read", err)
		_ = os.Remove(filepath.Join(dir, suggestionFile))
	case t.info.Scheduled != "":
		t.note("Turned down the agent's suggested project settings, nobody being there to weigh them")
		_ = writeAtomic(filepath.Join(dir, suggestedFile), []byte("refused"))
	default:
		t.info.Suggestion = partial
		t.p.m.saveL()
		t.publishL()
		t.note("Suggests project settings")
	}
}

// AnswerSuggestion is the user accepting the agent's suggested settings, or
// rejecting them.
func (t *Task) AnswerSuggestion(accept bool) error {
	t.lock()
	partial := t.info.Suggestion
	t.unlock()
	if partial == nil {
		return errors.New("That suggestion has been answered already")
	}
	note, said := "Suggested project settings rejected", "rejected"
	if accept {
		if err := t.p.SetConfig(partial); err != nil {
			return err
		}
		note, said = "Suggested project settings accepted", "accepted"
	}
	// Answered before it is put away, so that the services tick does not take
	// the suggestion up again meanwhile.
	t.lock()
	defer t.unlock()
	dir := filepath.Join(t.servicesDir(), askDir)
	if exists(filepath.Join(dir, suggestionFile)) {
		_ = writeAtomic(filepath.Join(dir, suggestedFile), []byte(said))
	} else {
		t.queueL("settings", fmt.Sprintf("The user %s the project settings you suggested.", said))
	}
	t.info.Suggestion = nil
	t.p.m.saveL()
	t.publishL()
	t.note(note)
	return nil
}
