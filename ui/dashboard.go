package ui

import (
	"encoding/json"
	"errors"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/vanviegen/tps/daemon"
)

// The dashboard's own state is the host list — which daemons to connect to —
// the names the user gave them, and the order the user put the projects in.
// The projects themselves belong to their host's daemon, so every dashboard
// using it sees the same ones, under the same names and colours; only their
// order spans hosts, and so lives here.

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

// dashboardFile is dashboard.json: the ssh destinations to connect to, the
// names given to hosts (by host id), the project order, and the names an older
// dashboard gave the projects there.
type dashboardFile struct {
	Hosts     []string          `json:"hosts"`
	HostNames map[string]string `json:"hostNames"`
	Order     []string          `json:"order"`
	Projects  []legacyProject   `json:"projects,omitempty"`
}

func loadHosts() (saved dashboardFile) {
	if data, err := os.ReadFile(configFile("dashboard.json")); err == nil {
		_ = json.Unmarshal(data, &saved)
		return saved
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
		saved.Hosts = append(saved.Hosts, h.Dest)
	}
	return saved
}

func saveHosts(saved dashboardFile) error {
	if saved.Hosts == nil {
		saved.Hosts = []string{}
	}
	if saved.HostNames == nil {
		saved.HostNames = map[string]string{}
	}
	if saved.Order == nil {
		saved.Order = []string{}
	}
	data, _ := json.MarshalIndent(saved, "", "\t")
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

// renameHostEntry puts another destination in a host's place in the list, and
// carries its name and the project order over to the host id that comes with it.
func (u *UI) renameHostEntry(old, dest string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	i := u.indexHostL(old)
	if i < 0 {
		return
	}
	u.hosts[i] = dest
	if name, ok := u.names[hostID(old)]; ok {
		delete(u.names, hostID(old))
		u.names[hostID(dest)] = name
	}
	was, now := hostID(old)+":", hostID(dest)+":"
	for j, id := range u.order {
		if pid, ok := strings.CutPrefix(id, was); ok {
			u.order[j] = now + pid
		}
	}
	u.saveL()
}

func (u *UI) removeHostEntry(dest string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if i := u.indexHostL(dest); i >= 0 {
		u.hosts = append(u.hosts[:i], u.hosts[i+1:]...)
		delete(u.names, hostID(dest))
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
	if err := saveHosts(dashboardFile{Hosts: u.hosts, HostNames: u.names, Order: u.order}); err != nil {
		log.Printf("saving the host list failed: %v", err)
	}
}

// nameHost gives a host the name it is shown by; an empty one gives it back
// the name its destination makes.
func (u *UI) nameHost(hid, name string) {
	u.mu.Lock()
	if name = strings.TrimSpace(name); name != "" {
		u.names[hid] = name
	} else {
		delete(u.names, hid)
	}
	u.saveL()
	u.mu.Unlock()
	u.publish()
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
	u.publish()
	return nil, nil
}

// publish puts the dashboard's own state in the state tree: the project order
// and the host names.
func (u *UI) publish() {
	u.mu.Lock()
	order := append([]string{}, u.order...)
	names := maps.Clone(u.names)
	u.mu.Unlock()
	u.hub.Set([]string{"projectOrder"}, order)
	u.hub.Set([]string{"hostNames"}, names)
}
