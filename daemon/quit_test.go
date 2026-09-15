package daemon

import (
	"path/filepath"
	"testing"
	"time"
)

// A daemon nobody is using stops itself; one that is being used, or that has
// something of its own to finish, does not.
func TestQuitIfUnused(t *testing.T) {
	exited := 0
	m := testManager()
	m.exit = func(int) { exited++ }
	m.usedAt = time.Now()

	m.quitIfUnused()
	if exited != 0 {
		t.Fatal("a daemon that has only just started should stay")
	}

	c := m.hub.AddClient()
	m.usedAt = time.Now().Add(-2 * unusedExit)
	m.quitIfUnused()
	if exited != 0 {
		t.Fatal("a daemon with a dashboard on it should stay")
	}

	m.hub.RemoveClient(c)
	m.quitIfUnused()
	if exited != 0 {
		t.Fatal("a daemon should sit out a reconnect, not go the moment the link drops")
	}

	unused := func() { m.usedAt = time.Now().Add(-2 * unusedExit) }

	unused()
	done := m.work()
	m.quitIfUnused()
	if exited != 0 {
		t.Fatal("a daemon with an operation in flight should stay")
	}
	done()

	p := newProject(m, "project", &ProjectInfo{Dir: "/tmp/project", Tasks: map[string]*TaskInfo{
		"1": {Phase: PhaseAgent, LimitUntil: time.Now().Add(time.Hour).UnixMilli()},
	}})
	p.tasks["1"] = newTask(p, "1", p.info.Tasks["1"])
	m.projects[p.pid] = p
	unused()
	m.quitIfUnused()
	if exited != 0 {
		t.Fatal("a daemon with a task waiting out a usage limit should stay")
	}
	delete(m.projects, p.pid)

	unused()
	m.quitIfUnused()
	if exited != 1 {
		t.Fatalf("an unused daemon should stop, exited %d times", exited)
	}
}

// The registry being written by something else (demo/seed.sh, an editor) stops
// the daemon, so that what is on disk is not undone by the next save.
func TestQuitIfConfigReplaced(t *testing.T) {
	newManager := func() (*Manager, *int) {
		exited := 0
		m := testManager()
		m.configFile = filepath.Join(t.TempDir(), "projects.json")
		m.exit = func(int) { exited++ }
		return m, &exited
	}

	m, exited := newManager()
	m.quitIfConfigReplaced()
	if *exited != 0 {
		t.Fatal("no registry, and none written: nothing has changed")
	}

	m, exited = newManager()
	write(t, m.configFile, `{"projects": []}`)
	m.saved = []byte(`{"projects": []}`)
	m.quitIfConfigReplaced()
	if *exited != 0 {
		t.Fatal("the registry this daemon wrote itself should not stop it")
	}
	write(t, m.configFile, `{"projects": [{"dir": "/tmp/elsewhere"}]}`)
	m.quitIfConfigReplaced()
	if *exited != 1 {
		t.Fatalf("a registry written by someone else should stop the daemon, exited %d times", *exited)
	}

	m, exited = newManager()
	m.saved = []byte(`{"projects": []}`)
	m.quitIfConfigReplaced()
	if *exited != 1 {
		t.Fatal("a registry taken away should stop the daemon too")
	}
}
