package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vanviegen/agent-manager/daemon"
)

func (u *UI) addHostLink(h HostConfig) *Link {
	return u.addLink(h.ID, h.Dest, newSSHTransport(u, h.ID, h.Dest))
}

// addHost registers an SSH destination: it must connect (prompting as
// needed) and pass the host checks before it is saved.
func (u *UI) addHost(raw json.RawMessage) (any, error) {
	var args struct {
		Dest string `json:"dest"`
	}
	_ = json.Unmarshal(raw, &args)
	dest := strings.TrimSpace(args.Dest)
	if dest == "" {
		return nil, errors.New("Give an SSH destination, like user@host")
	}
	u.mu.Lock()
	for _, l := range u.links {
		if l.dest == dest {
			u.mu.Unlock()
			return nil, fmt.Errorf("%s is already registered", dest)
		}
	}
	hid := daemon.Slugify(dest)
	for u.links[hid] != nil || hid == "local" {
		hid += "x"
	}
	u.mu.Unlock()
	l := u.addHostLink(HostConfig{ID: hid, Dest: dest})
	select {
	case err := <-l.first:
		if err != nil {
			u.dropLink(l)
			return nil, err
		}
	case <-time.After(10 * time.Minute):
		u.dropLink(l)
		return nil, errors.New("connecting took too long")
	}
	u.mu.Lock()
	hosts := loadHosts()
	hosts = append(hosts, HostConfig{ID: hid, Dest: dest})
	err := saveHosts(hosts)
	u.mu.Unlock()
	return map[string]any{"hid": hid}, err
}

func (u *UI) removeHost(raw json.RawMessage) (any, error) {
	var args struct {
		Hid string `json:"hid"`
	}
	_ = json.Unmarshal(raw, &args)
	if args.Hid == "local" {
		return nil, errors.New("This machine cannot be removed")
	}
	l, err := u.link(args.Hid)
	if err != nil {
		return nil, err
	}
	u.dropLink(l)
	u.mu.Lock()
	defer u.mu.Unlock()
	var hosts []HostConfig
	for _, h := range loadHosts() {
		if h.ID != args.Hid {
			hosts = append(hosts, h)
		}
	}
	return nil, saveHosts(hosts)
}

// dropLink stops a link and removes everything it mirrored.
func (u *UI) dropLink(l *Link) {
	u.mu.Lock()
	delete(u.links, l.hid)
	u.mu.Unlock()
	l.close()
	if projects, ok := u.hub.Get("projects").(map[string]any); ok {
		for cpid := range projects {
			if strings.HasPrefix(cpid, l.hid+"~") {
				u.hub.Set([]string{"projects", cpid}, nil)
			}
		}
	}
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
