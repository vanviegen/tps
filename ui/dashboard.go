package ui

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/vanviegen/agent-manager/daemon"
)

// The dashboard's own state is the host list: which daemons to connect to.
// The projects on a host belong to that host's daemon, so every dashboard
// using it sees the same ones, under the same names.

func configFile(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tps", name)
}

// legacyProject is a project as older dashboards listed them: their names are
// carried over to the daemons that own the projects now, once each connects.
type legacyProject struct {
	Host string `json:"host"`
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

// loadHosts reads the ssh destinations to connect to, and the names an older
// dashboard gave the projects there.
func loadHosts() (hosts []string, names []legacyProject) {
	var saved struct {
		Hosts    []string        `json:"hosts"`
		Projects []legacyProject `json:"projects"`
	}
	if data, err := os.ReadFile(configFile("dashboard.json")); err == nil {
		_ = json.Unmarshal(data, &saved)
		return saved.Hosts, saved.Projects
	}
	// Older still: hosts.json, with the hosts as objects.
	var older struct {
		Hosts []struct {
			Dest string `json:"dest"`
		} `json:"hosts"`
	}
	if data, err := os.ReadFile(configFile("hosts.json")); err == nil {
		_ = json.Unmarshal(data, &older)
	}
	for _, h := range older.Hosts {
		hosts = append(hosts, h.Dest)
	}
	return hosts, nil
}

func saveHosts(hosts []string) error {
	if hosts == nil {
		hosts = []string{}
	}
	data, _ := json.MarshalIndent(map[string]any{"hosts": hosts}, "", "\t")
	path := configFile("dashboard.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// hostID names a host in the state tree and in urls.
func hostID(dest string) string {
	if dest == "" {
		return "local"
	}
	return daemon.Slugify(dest)
}

// --- the host list: the ssh destinations the dashboard shows ---

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

func (u *UI) saveL() {
	if err := saveHosts(u.hosts); err != nil {
		log.Printf("saving the host list failed: %v", err)
	}
}
