package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// addProject has a host's daemon register a repository at "dir", connecting to
// it first (prompting as needed). The daemon owns the list, so the project
// shows up on every dashboard using that host.
func (u *UI) addProject(raw json.RawMessage) (any, error) {
	var args struct {
		Hid  string `json:"hid"`
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &args)
	l, err := u.link(args.Hid)
	if err != nil {
		return nil, err
	}
	dir := strings.TrimSpace(args.Dir)
	if dir == "" {
		return nil, errors.New("Give a directory, like ~/projects/app")
	}
	l.Wake()
	if err := l.awaitConnection(10 * time.Minute); err != nil {
		return nil, err
	}
	res, err := l.cmd("addProject", map[string]any{"dir": dir, "name": strings.TrimSpace(args.Name)})
	if err != nil {
		return nil, err
	}
	return l.newProject(res)
}

// turnIntoProject is a task command like those forwarded as they are, but
// what it answers with is a project the dashboard has yet to hear of.
func (u *UI) turnIntoProject(raw json.RawMessage) (any, error) {
	args := map[string]any{}
	_ = json.Unmarshal(raw, &args)
	id, _ := args["pid"].(string)
	l, pid, err := u.resolve(id)
	if err != nil {
		return nil, err
	}
	args["pid"] = pid
	res, err := l.cmd("turnIntoProject", args)
	if err != nil {
		return nil, err
	}
	return l.newProject(res)
}

// newProject reads a daemon's answer naming a project it just made, and a
// task in it if there is one. The patch announcing the project may not have
// arrived yet; mirroring it here as well means the dashboard can go straight
// to it.
func (l *Link) newProject(res json.RawMessage) (any, error) {
	var out struct {
		Pid     string         `json:"pid"`
		Tid     string         `json:"tid,omitempty"`
		Project map[string]any `json:"project,omitempty"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	l.mirror(out.Pid, out.Project)
	out.Pid, out.Project = l.id(out.Pid), nil
	return out, nil
}

// --- host commands, by host id ---

// addHost lists an ssh destination and starts connecting to it; what comes of
// that shows up as the host's status. Its daemon's projects come along with
// it, so a host set up from another dashboard arrives complete.
func (u *UI) addHost(raw json.RawMessage) (any, error) {
	var args struct {
		Dest string `json:"dest"`
	}
	_ = json.Unmarshal(raw, &args)
	dest, err := u.listable(args.Dest, "")
	if err != nil {
		return nil, err
	}
	u.addHostEntry(dest)
	u.linkTo(dest, nil).Wake()
	return map[string]any{"hid": hostID(dest)}, nil
}

// setHost names a host and points it at another ssh destination — a machine
// that moved, a port that changed. The old link is dropped and a new one takes
// its place in the list, connecting like a freshly added host and bringing
// along the projects it finds there, in the place the old one's had in the
// sidebar. This machine only takes a name.
func (u *UI) setHost(raw json.RawMessage) (any, error) {
	var args struct {
		Hid  string `json:"hid"`
		Dest string `json:"dest"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &args)
	l, err := u.link(args.Hid)
	if err != nil {
		return nil, err
	}
	u.nameHost(l.hid, args.Name)
	if l.dest == "" || strings.TrimSpace(args.Dest) == l.dest {
		return map[string]any{"hid": l.hid}, nil
	}
	dest, err := u.listable(args.Dest, l.hid)
	if err != nil {
		return nil, err
	}
	u.unlist(l)
	u.renameHostEntry(l.dest, dest)
	u.publish()
	u.linkTo(dest, nil).Wake()
	return map[string]any{"hid": hostID(dest)}, nil
}

// listable trims a destination and says whether it can go on the list: it has
// to say something, and no host but the one it replaces can be there under the
// id it would get.
func (u *UI) listable(raw, replacing string) (string, error) {
	dest := strings.TrimSpace(raw)
	if dest == "" {
		return "", errors.New("Give a host, like user@host or an alias from ~/.ssh/config")
	}
	if existing, err := u.link(hostID(dest)); err == nil && existing.hid != replacing {
		if existing.dest == dest {
			return "", fmt.Errorf("%s is already listed", dest)
		}
		return "", fmt.Errorf("%s is too much like %s; use an alias from ~/.ssh/config", dest, existing.dest)
	}
	return dest, nil
}

// removeHost takes a host off the list, its projects with it. Its daemon and
// their tasks stay as they are, and come back when the host is added again.
func (u *UI) removeHost(raw json.RawMessage) (any, error) {
	var args struct {
		Hid string `json:"hid"`
	}
	_ = json.Unmarshal(raw, &args)
	l, err := u.link(args.Hid)
	if err != nil {
		return nil, err
	}
	if l.dest == "" {
		return nil, errors.New("This machine is always listed")
	}
	u.unlist(l)
	u.removeHostEntry(l.dest)
	return nil, nil
}

// unlist takes a host's link off the board: its projects go, the link is
// dropped and closed, and the host leaves the state tree. What is left is the
// entry in the host list, which the caller either removes or points elsewhere.
func (u *UI) unlist(l *Link) {
	l.dropAll()
	u.mu.Lock()
	delete(u.links, l.hid)
	u.mu.Unlock()
	l.close()
	u.hub.Set([]string{"hosts", l.hid}, nil)
}

func (u *UI) hostCmd(raw json.RawMessage, f func(l *Link) error) (any, error) {
	var args struct {
		Hid string `json:"hid"`
	}
	_ = json.Unmarshal(raw, &args)
	l, err := u.link(args.Hid)
	if err != nil {
		return nil, err
	}
	return nil, f(l)
}

func (u *UI) connectHost(raw json.RawMessage) (any, error) {
	return u.hostCmd(raw, func(l *Link) error { l.Wake(); return nil })
}

func (u *UI) updateDaemon(raw json.RawMessage) (any, error) {
	return u.hostCmd(raw, func(l *Link) error { return l.upgrade() })
}

// setOpenRouterKey hands the host's daemon the management key it makes agents'
// OpenRouter keys with (see daemon/openrouter.go).
func (u *UI) setOpenRouterKey(raw json.RawMessage) (any, error) {
	var args struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(raw, &args)
	return u.hostCmd(raw, func(l *Link) error {
		_, err := l.cmd("setOpenRouterKey", map[string]any{"text": args.Key})
		return err
	})
}

func (u *UI) stopDaemon(raw json.RawMessage) (any, error) {
	return u.hostCmd(raw, func(l *Link) error {
		l.mu.Lock()
		l.stopped = true
		l.mu.Unlock()
		_, err := l.cmd("stop", map[string]any{})
		return err
	})
}
