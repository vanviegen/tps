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
	var out struct {
		Pid     string         `json:"pid"`
		Project map[string]any `json:"project"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	// The patch announcing it may not have arrived yet; mirroring it here as
	// well means the dashboard can go straight to the project.
	l.mirror(out.Pid, out.Project)
	return map[string]any{"pid": l.id(out.Pid)}, nil
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
	dest := strings.TrimSpace(args.Dest)
	if dest == "" {
		return nil, errors.New("Give a host, like user@host or an alias from ~/.ssh/config")
	}
	if existing, err := u.link(hostID(dest)); err == nil {
		if existing.dest == dest {
			return nil, fmt.Errorf("%s is already listed", dest)
		}
		return nil, fmt.Errorf("%s is too much like %s; use an alias from ~/.ssh/config", dest, existing.dest)
	}
	u.addHostEntry(dest)
	u.linkTo(dest, nil).Wake()
	return map[string]any{"hid": hostID(dest)}, nil
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
	l.dropAll()
	u.removeHostEntry(l.dest)
	u.mu.Lock()
	delete(u.links, l.hid)
	u.mu.Unlock()
	l.close()
	u.hub.Set([]string{"hosts", l.hid}, nil)
	return nil, nil
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

func (u *UI) copyCredentials(raw json.RawMessage) (any, error) {
	return u.hostCmd(raw, func(l *Link) error {
		t, ok := l.tr.(*sshTransport)
		if !ok {
			return errors.New("This machine already has your claude login")
		}
		return t.CopyCredentials()
	})
}

func (u *UI) updateDaemon(raw json.RawMessage) (any, error) {
	return u.hostCmd(raw, func(l *Link) error { return l.upgrade() })
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
