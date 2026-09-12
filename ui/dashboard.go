package ui

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/vanviegen/tps/daemon"
)

// The dashboard's own state is the host list — which daemons to connect to —
// and the order the user put the projects in. The projects themselves belong
// to their host's daemon, so every dashboard using it sees the same ones,
// under the same names and colours; only their order spans hosts, and so
// lives here.

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

// loadHosts reads the ssh destinations to connect to, the project order, and
// the names an older dashboard gave the projects there.
func loadHosts() (hosts, order []string, names []legacyProject) {
	var saved struct {
		Hosts    []string        `json:"hosts"`
		Order    []string        `json:"order"`
		Projects []legacyProject `json:"projects"`
	}
	if data, err := os.ReadFile(configFile("dashboard.json")); err == nil {
		_ = json.Unmarshal(data, &saved)
		return saved.Hosts, saved.Order, saved.Projects
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
	return hosts, nil, nil
}

func saveHosts(hosts, order []string) error {
	if hosts == nil {
		hosts = []string{}
	}
	if order == nil {
		order = []string{}
	}
	data, _ := json.MarshalIndent(map[string]any{"hosts": hosts, "order": order}, "", "\t")
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
	if err := saveHosts(u.hosts, u.order); err != nil {
		log.Printf("saving the host list failed: %v", err)
	}
}

// --- the project order: ids ("<host id>:<pid>") as the user arranged them ---

// setProjectOrder stores the order the sidebar's projects were put in, and
// publishes it. Projects it does not name follow the named ones, by name.
func (u *UI) setProjectOrder(raw json.RawMessage) (any, error) {
	var args struct {
		Order []string `json:"order"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, errors.New("bad arguments")
	}
	seen := map[string]bool{}
	order := []string{}
	for _, id := range args.Order {
		if id != "" && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	u.mu.Lock()
	u.order = order
	u.saveL()
	u.mu.Unlock()
	u.publishOrder()
	return nil, nil
}

func (u *UI) publishOrder() {
	u.mu.Lock()
	order := append([]string{}, u.order...)
	u.mu.Unlock()
	u.hub.Set([]string{"projectOrder"}, order)
}
