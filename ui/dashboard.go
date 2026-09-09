package ui

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/vanviegen/agent-manager/daemon"
)

// ProjectEntry is one project this dashboard shows: a directory on a host,
// under the name the user gave it. The daemon on that host owns the tasks;
// other dashboards may list the same project, or others of that daemon.
type ProjectEntry struct {
	ID   string `json:"id"`
	Host string `json:"host,omitempty"` // an ssh destination; empty for this machine
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

func configFile(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tps", name)
}

// loadDashboard reads the host and project lists; found is false when nothing
// was saved yet.
func loadDashboard() (entries []ProjectEntry, hosts []string, found bool) {
	var saved struct {
		Hosts    []string       `json:"hosts"`
		Projects []ProjectEntry `json:"projects"`
	}
	data, err := os.ReadFile(configFile("dashboard.json"))
	if err != nil {
		return nil, nil, false
	}
	_ = json.Unmarshal(data, &saved)
	return saved.Projects, saved.Hosts, true
}

func saveDashboard(entries []ProjectEntry, hosts []string) error {
	if entries == nil {
		entries = []ProjectEntry{}
	}
	if hosts == nil {
		hosts = []string{}
	}
	data, _ := json.MarshalIndent(map[string]any{"hosts": hosts, "projects": entries}, "", "\t")
	path := configFile("dashboard.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// legacyHosts lists the hosts of a dashboard from before it kept the project
// list itself (hosts.json); their projects are imported once.
func legacyHosts() []string {
	var saved struct {
		Hosts []struct {
			Dest string `json:"dest"`
		} `json:"hosts"`
	}
	if data, err := os.ReadFile(configFile("hosts.json")); err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	var dests []string
	for _, h := range saved.Hosts {
		dests = append(dests, h.Dest)
	}
	return dests
}

// hostID names a host in the state tree and in urls.
func hostID(dest string) string {
	if dest == "" {
		return "local"
	}
	return daemon.Slugify(dest)
}

// skeleton is what a project looks like before its daemon has been heard from.
func skeleton(e ProjectEntry) map[string]any {
	return map[string]any{"name": e.Name, "host": hostID(e.Host), "dir": e.Dir}
}

func (u *UI) entry(id string) (ProjectEntry, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, e := range u.entries {
		if e.ID == id {
			return e, true
		}
	}
	return ProjectEntry{}, false
}

// entriesOn lists the projects on a host, or the one at dir there.
func (u *UI) entriesOn(dest, dir string) []ProjectEntry {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []ProjectEntry
	for _, e := range u.entries {
		if e.Host == dest && (dir == "" || e.Dir == dir) {
			out = append(out, e)
		}
	}
	return out
}

// addEntry lists a project under an id made from its name, and saves.
func (u *UI) addEntry(dest, dir, name string) ProjectEntry {
	u.mu.Lock()
	defer u.mu.Unlock()
	if name == "" {
		name = filepath.Base(dir)
	}
	id := daemon.Slugify(name)
	for u.indexL(id) >= 0 {
		id += "x"
	}
	e := ProjectEntry{ID: id, Host: dest, Dir: dir, Name: name}
	u.entries = append(u.entries, e)
	u.saveL()
	return e
}

func (u *UI) updateEntry(id string, f func(e *ProjectEntry)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if i := u.indexL(id); i >= 0 {
		f(&u.entries[i])
		u.saveL()
	}
}

func (u *UI) removeEntry(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if i := u.indexL(id); i >= 0 {
		u.entries = append(u.entries[:i], u.entries[i+1:]...)
		u.saveL()
	}
}

func (u *UI) indexL(id string) int {
	for i, e := range u.entries {
		if e.ID == id {
			return i
		}
	}
	return -1
}

func (u *UI) saveL() {
	if err := saveDashboard(u.entries, u.hosts); err != nil {
		log.Printf("saving the project list failed: %v", err)
	}
}

// --- the host list: the ssh destinations the dashboard shows, projects or not ---

func (u *UI) addHostEntry(dest string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.indexHostL(dest) < 0 {
		u.hosts = append(u.hosts, dest)
		u.saveL()
	}
}

func (u *UI) removeHostEntry(dest string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if i := u.indexHostL(dest); i >= 0 {
		u.hosts = append(u.hosts[:i], u.hosts[i+1:]...)
		u.saveL()
	}
}

func (u *UI) indexHostL(dest string) int {
	for i, d := range u.hosts {
		if d == dest {
			return i
		}
	}
	return -1
}
