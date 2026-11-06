package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// A new workspace starts with the seeds the checkout has; what git tracks is
// git's, and a seed the checkout lacks is no matter.
func TestSeed(t *testing.T) {
	task, origin := testTask(t)
	if err := os.WriteFile(filepath.Join(origin, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "--quiet", "-m", "ignore node_modules")
	if err := os.MkdirAll(filepath.Join(origin, "node_modules", "left-pad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "node_modules", "left-pad", "index.js"), []byte("pad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task.p.info.Seeds = []string{"node_modules", "a.txt", "dist"}
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	repo := task.repoDir()
	if got := readFile(filepath.Join(repo, "node_modules", "left-pad", "index.js")); got != "pad\n" {
		t.Errorf("the seed was not copied: %q", got)
	}
	if got := readFile(filepath.Join(repo, "a.txt")); got != "one\n" {
		t.Errorf("a tracked file was seeded over: %q", got)
	}
	if note := task.lastNote(); note != "" {
		t.Errorf("seeding noted %q", note)
	}
}

// An agent's suggestion waits for the user, the tool with it, and is
// accepted into the project's settings; one answered after the tool gave up
// is told the agent the next time instead.
func TestSuggestSettings(t *testing.T) {
	task, _ := testTask(t)
	task.p.info.Mounts = []Mount{{Path: "~/.npmrc"}}
	if err := os.MkdirAll(task.servicesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	st := &guestTool{root: task.servicesDir()}
	for _, bad := range []string{`{"bogus": 1}`, `{"seeds": ["../elsewhere"]}`, `{"seeds": [".git/hooks"]}`,
		`{"defaults": {"onReady": "never"}}`, `{"color": "red"}`, `{"name": " "}`, `{"seeds": "node_modules"}`} {
		if st.settings([]string{"suggest", bad}) == 0 {
			t.Errorf("%s was taken as a suggestion", bad)
		}
	}
	if task.p.SetConfig(map[string]any{"name": "Renamed", "seeds": []any{"/etc"}}) == nil || task.p.info.Name == "Renamed" {
		t.Error("settings with a bad seed in them were taken in part")
	}
	sync := func() {
		task.lock()
		task.syncSettingsL()
		task.unlock()
	}
	answered := make(chan int)
	go func() {
		answered <- st.settings([]string{"suggest", `{"name": "Renamed", "autoDeploy": true, "seeds": ["node_modules/", " "], "defaults": {"onReady": "merge"}}`})
	}()
	for task.info.Suggestion == nil {
		time.Sleep(50 * time.Millisecond)
		sync()
	}
	if code := st.settings(nil); code != 0 {
		t.Errorf("the settings cannot be read: %d", code)
	}
	if err := task.AnswerSuggestion(true); err != nil {
		t.Fatal(err)
	}
	if code := <-answered; code != 0 {
		t.Errorf("an accepted suggestion exits %d", code)
	}
	info := task.p.info
	if info.Name != "Renamed" || !info.AutoDeploy || !reflect.DeepEqual(info.Seeds, []string{"node_modules"}) || len(info.Mounts) != 1 || info.Defaults.OnReady != AnswerMerge {
		t.Errorf("accepted into %q, %v, %q, %v, %q", info.Name, info.AutoDeploy, info.Seeds, info.Mounts, info.Defaults.OnReady)
	}
	if len(task.info.Pending) != 0 {
		t.Errorf("the agent is told what it waited to hear: %v", task.info.Pending)
	}
	if task.AnswerSuggestion(false) == nil {
		t.Error("a suggestion was answered twice")
	}

	// The tool gave up: what it leaves behind is the suggestion in the task.
	task.info.Suggestion = map[string]any{"autoDeploy": false}
	if err := task.AnswerSuggestion(false); err != nil {
		t.Fatal(err)
	}
	if !info.AutoDeploy || len(task.info.Pending) != 1 || task.info.Pending[0].Key != "settings" {
		t.Errorf("a late rejection: autoDeploy %v, told %v", info.AutoDeploy, task.info.Pending)
	}
}
