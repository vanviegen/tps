package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// parseSpec splits "host:dir" (dir being a path on that ssh host) from a
// plain local dir. A path has a slash before any colon it contains.
func parseSpec(spec string) (dest, dir string) {
	spec = strings.TrimSpace(spec)
	if i := strings.Index(spec, ":"); i > 0 && !strings.Contains(spec[:i], "/") {
		return strings.TrimSpace(spec[:i]), strings.TrimSpace(spec[i+1:])
	}
	return "", spec
}

// addProject lists a repository: "dir" on this machine, or "host:dir" on an
// ssh host, which is connected first (prompting as needed).
func (u *UI) addProject(raw json.RawMessage) (any, error) {
	var args struct {
		Spec string `json:"spec"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &args)
	dest, dir := parseSpec(args.Spec)
	if dir == "" {
		return nil, errors.New("Give a directory, like ~/projects/app or user@host:~/projects/app")
	}
	l := u.linkTo(dest, false)
	if l.dest != dest {
		return nil, fmt.Errorf("%s is too much like %s; use an alias from ~/.ssh/config", dest, l.dest)
	}
	l.Wake()
	if err := l.awaitConnection(10 * time.Minute); err != nil {
		u.dropIfUnused(l)
		return nil, err
	}
	type result struct {
		e   ProjectEntry
		err error
	}
	ch := make(chan result, 1)
	l.register(dir, func(pid, dir string, project map[string]any, err error) {
		if err == nil {
			if listed := u.entriesOn(dest, dir); len(listed) > 0 {
				err = fmt.Errorf("%s is already listed, as %s", dir, listed[0].Name)
			}
		}
		if err != nil {
			ch <- result{err: err}
			return
		}
		e := u.addEntry(dest, dir, strings.TrimSpace(args.Name))
		l.mount(e.ID, pid, project)
		ch <- result{e: e}
	})
	r := <-ch
	if r.err != nil {
		u.dropIfUnused(l)
		return nil, r.err
	}
	return map[string]any{"pid": r.e.ID}, nil
}

// removeProject drops a project from the list. The daemon keeps it, with its
// tasks, unless it has none.
func (u *UI) removeProject(raw json.RawMessage) (any, error) {
	var args struct {
		Pid string `json:"pid"`
	}
	_ = json.Unmarshal(raw, &args)
	e, ok := u.entry(args.Pid)
	if !ok {
		return nil, errors.New("Unknown project: " + args.Pid)
	}
	var tasks map[string]json.RawMessage
	_ = json.Unmarshal(u.hub.Snapshot("projects", e.ID, "tasks"), &tasks)
	u.removeEntry(e.ID)
	u.hub.Set([]string{"projects", e.ID}, nil)
	if l, err := u.link(hostID(e.Host)); err == nil {
		if pid, ok := l.unmount(e.ID); ok && len(tasks) == 0 {
			_, _ = l.cmd("removeProject", map[string]any{"pid": pid})
		}
		u.dropIfUnused(l)
	}
	return nil, nil
}

func (u *UI) renameProject(raw json.RawMessage) (any, error) {
	var args struct {
		Pid  string `json:"pid"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &args)
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("Give the project a name")
	}
	u.updateEntry(args.Pid, func(e *ProjectEntry) { e.Name = name })
	u.hub.Set([]string{"projects", args.Pid, "name"}, name)
	return nil, nil
}

// dropIfUnused stops the link to a host no listed project lives on.
func (u *UI) dropIfUnused(l *Link) {
	if len(u.entriesOn(l.dest, "")) > 0 {
		return
	}
	u.mu.Lock()
	delete(u.links, l.hid)
	u.mu.Unlock()
	l.close()
	u.hub.Set([]string{"hosts", l.hid}, nil)
}

// --- host commands, by host id ---

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
