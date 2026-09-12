package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vanviegen/tps/hub"
)

func logf(format string, args ...any) { log.Printf(format, args...) }

// Manager is the registry of projects and tasks, persisted to
// ~/.config/tps/projects.json. Its mutex guards all in-memory task and
// project state; I/O happens outside it.
type Manager struct {
	mu         sync.Mutex
	projects   map[string]*Project
	dataDir    string
	configFile string
	hub        *hub.Hub
	saveCh     chan []byte
	exit       func(code int)
	busy       atomic.Int32 // long operations in flight (builds, clones, merges, deletions)
	restarting bool

	modelsMu     sync.Mutex // guards the model detection
	modelsFound  bool
	modelsFailed bool
}

// work brackets an operation a restart must not interrupt.
func (m *Manager) work() func() {
	m.busy.Add(1)
	return func() { m.busy.Add(-1) }
}

// scheduleRestart makes the daemon exit as soon as nothing is running; the
// UI then starts the binary it wants. Containers stay up and are adopted.
func (m *Manager) scheduleRestart() {
	m.mu.Lock()
	pending := m.restarting
	m.restarting = true
	m.mu.Unlock()
	if pending {
		return
	}
	m.hub.Set([]string{"restarting"}, true)
	go func() {
		for range time.Tick(2 * time.Second) {
			if m.busy.Load() == 0 && !m.anyWorking() {
				logf("restarting now that nothing is running")
				m.exit(0)
			}
		}
	}()
}

func NewManager(h *hub.Hub, exit func(code int)) *Manager {
	m := &Manager{
		projects:   map[string]*Project{},
		dataDir:    filepath.Join(home(), ".local", "share", "tps"),
		configFile: filepath.Join(home(), ".config", "tps", "projects.json"),
		hub:        h,
		saveCh:     make(chan []byte, 1),
		exit:       exit,
	}
	// A watch is a dashboard holding something open: a task (its chat
	// streams, its workspace stays up, and VS Code runs in it), or, under the
	// key "<pid>/-", the project's own checkout in VS Code. The last watcher
	// going — a Close, or a dashboard's connection dropping — takes VS Code
	// down with it; nothing else does.
	h.OnWatch = func(key string, count int) {
		pid, tid, _ := strings.Cut(key, "/")
		m.mu.Lock()
		p := m.projects[pid]
		if p == nil {
			m.mu.Unlock()
			return
		}
		if tid == "-" {
			p.codeWanted = count > 0
			m.mu.Unlock()
			p.syncCode()
			return
		}
		if t := p.tasks[tid]; t != nil {
			if count > 0 && t.viewers == 0 {
				go t.refreshChanges()
			}
			t.viewers = count
			t.touchL()
			t.setCodeWantedL(count > 0)
			if count == 0 {
				p.autoStartL() // closing a plan lets a task that was waiting for it go
			}
		}
		m.mu.Unlock()
	}
	go m.saver()
	return m
}

func (m *Manager) Start() error {
	var saved struct {
		Projects []*ProjectInfo `json:"projects"`
	}
	if data, err := os.ReadFile(m.configFile); err == nil {
		if err := json.Unmarshal(data, &saved); err != nil {
			return fmt.Errorf("%s: %w", m.configFile, err)
		}
	}
	for _, info := range saved.Projects {
		if _, err := m.load(info); err != nil {
			logf("Failed to load project %s: %v", info.Dir, err)
		}
	}
	m.hub.Set([]string{"ready"}, true)
	go m.refreshModels()
	go m.ticker(60*time.Second, m.refreshModels) // until claude answers
	go m.ticker(60*time.Second, m.sweep)
	go m.ticker(2*time.Second, m.checkLive)
	go m.ticker(5*time.Second, m.refreshWatched)
	go m.ticker(time.Second, m.syncServices)
	go m.inhibitLoop()
	return nil
}

func (m *Manager) ticker(every time.Duration, f func()) {
	for range time.Tick(every) {
		f()
	}
}

// saveL snapshots the registry; the saver goroutine writes the newest snapshot.
func (m *Manager) saveL() {
	projects := make([]*ProjectInfo, 0, len(m.projects))
	for _, p := range m.sortedProjectsL() {
		projects = append(projects, p.info)
	}
	data, err := json.MarshalIndent(map[string]any{"projects": projects}, "", "\t")
	if err != nil {
		return
	}
	select {
	case m.saveCh <- data:
	default:
		<-m.saveCh
		m.saveCh <- data
	}
}

func (m *Manager) saver() {
	for data := range m.saveCh {
		_ = os.MkdirAll(filepath.Dir(m.configFile), 0o755)
		if err := os.WriteFile(m.configFile, append(data, '\n'), 0o644); err != nil {
			logf("save failed: %v", err)
		}
	}
}

func (m *Manager) sortedProjectsL() []*Project {
	out := make([]*Project, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, p)
	}
	// Registration order is what the JSON file had; keep it stable by pid.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].pid < out[j-1].pid; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (m *Manager) load(info *ProjectInfo) (*Project, error) {
	m.mu.Lock()
	pid := Slugify(filepath.Base(info.Dir))
	for m.projects[pid] != nil {
		pid += "x"
	}
	p := newProject(m, pid, info)
	m.projects[pid] = p
	m.mu.Unlock()
	if err := p.init(); err != nil {
		m.mu.Lock()
		delete(m.projects, pid)
		m.mu.Unlock()
		m.hub.Set([]string{"projects", pid}, nil)
		return nil, err
	}
	return p, nil
}

// Add registers a git repository (a path relative to the home directory, or
// absolute) under the name given, or returns it as it is when it is already
// registered: the list is the host's, and dashboards share it.
func (m *Manager) Add(dir, name string) (*Project, error) {
	if dir == "~" {
		dir = ""
	}
	dir = strings.TrimPrefix(dir, "~/")
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(home(), dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("No such directory: %s", dir)
	}
	dir = real
	m.mu.Lock()
	for _, p := range m.projects {
		if p.dir() == dir {
			m.mu.Unlock()
			return p, nil
		}
	}
	m.mu.Unlock()
	if !gitOK(dir, "rev-parse", "--git-dir") {
		return nil, fmt.Errorf("%s is not a git repository", dir)
	}
	if !gitOK(dir, "rev-parse", "--verify", "-q", "HEAD") {
		return nil, fmt.Errorf("%s has no commits yet", dir)
	}
	p, err := m.load(&ProjectInfo{Dir: dir, Name: strings.TrimSpace(name)})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.saveL()
	m.mu.Unlock()
	return p, nil
}

// Remove unregisters a project, with the tasks and workspaces it holds. The
// repository itself is left alone.
func (m *Manager) Remove(pid string) error {
	p, err := m.project(pid)
	if err != nil {
		return err
	}
	p.forget()
	_ = os.RemoveAll(p.tasksDir()) // else a project added later under the same pid inherits it
	m.mu.Lock()
	delete(m.projects, pid)
	m.saveL()
	m.mu.Unlock()
	m.hub.Set([]string{"projects", pid}, nil)
	return nil
}

func (m *Manager) project(pid string) (*Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.projects[pid]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("Unknown project: %s", pid)
}

func (m *Manager) task(pid, tid string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.projects[pid]; p != nil {
		if t := p.tasks[tid]; t != nil {
			return t, nil
		}
	}
	return nil, fmt.Errorf("Unknown task: %s/%s", pid, tid)
}

func (m *Manager) allTasksL() []*Task {
	var out []*Task
	for _, p := range m.projects {
		out = append(out, p.taskListL()...)
	}
	return out
}

// sweep is the minute's housekeeping: the 'dirty' flags, the auto-starts no
// event announced (a daemon that restarted with tasks already waiting), and
// the workspaces nobody is using.
func (m *Manager) sweep() {
	m.mu.Lock()
	projects := m.sortedProjectsL()
	m.mu.Unlock()
	for _, p := range projects {
		p.refreshMeta() // keep the 'dirty' flag current
	}
	m.mu.Lock()
	for _, p := range m.projects {
		p.autoStartL()
	}
	now := time.Now().UnixMilli()
	var idle, unheld, overdue []*Task
	for _, t := range m.allTasksL() {
		// A usage-limit wait about to be up is left alone: it starts a turn of
		// its own in a moment, which a workspace torn down under it would not
		// survive. One well past its time slept through a suspend — the
		// monotonic clock its timer runs on stops with the machine — and is
		// caught up here.
		if t.info.LimitUntil > 0 && now > t.info.LimitUntil-2*time.Minute.Milliseconds() {
			if now > t.info.LimitUntil+time.Minute.Milliseconds() {
				overdue = append(overdue, t)
			}
		} else if t.status == StatusUp && !t.workingL() && t.viewers == 0 && time.Since(t.lastActivity) > idleShutdown {
			idle = append(idle, t)
		} else if t.codeUp && t.viewers == 0 && !t.codeWanted {
			// A code-server adopted from before a restart that no dashboard came back for.
			unheld = append(unheld, t)
		}
	}
	m.mu.Unlock()
	for _, t := range overdue {
		go t.resumeAfterLimit()
	}
	for _, t := range idle {
		t.note("workspace idle, shutting down (your work is untouched)")
		t.queue("container", idlePrompt)
		t.down()
	}
	for _, t := range unheld {
		go t.syncCode()
	}
}

var liveClient = &http.Client{Timeout: 1500 * time.Millisecond}

// portProbe is what a round found on one published port.
type portProbe struct {
	Open bool // something listens inside the container
	HTTP bool // and answers HTTP: the port is worth a browser tab
}

// checkLive probes each running task's published ports. One round per task
// at a time, all of its ports at once.
func (m *Manager) checkLive() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.allTasksL() {
		if t.status != StatusUp || t.container == nil || t.checkingLive || len(t.container.Ports) == 0 {
			continue
		}
		t.checkingLive = true
		c := t.container
		go func() {
			live := make(map[int]portProbe, len(c.Ports))
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, p := range c.Ports {
				wg.Add(1)
				go func() {
					defer wg.Done()
					probe := probePort(p.Host)
					mu.Lock()
					live[p.Port] = probe
					mu.Unlock()
				}()
			}
			wg.Wait()
			m.mu.Lock()
			t.checkingLive = false
			if t.container == c && t.status == StatusUp {
				t.setLiveL(live)
			}
			m.mu.Unlock()
		}()
	}
}

// probePort: does something listen behind this host port, and is it HTTP?
// Podman's port forwarder accepts every connection itself and only then
// tries the container, so accepting says nothing; a connection that stays
// open (or greets us) does, where one closed at once means nothing listened
// inside.
func probePort(port int) portProbe {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return portProbe{}
	}
	_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	_, err = conn.Read(make([]byte, 1))
	conn.Close()
	var nerr net.Error
	if err != nil && !(errors.As(err, &nerr) && nerr.Timeout()) {
		return portProbe{}
	}
	return portProbe{Open: true, HTTP: answersHTTP(port)}
}

func answersHTTP(port int) bool {
	resp, err := liveClient.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// refreshWatched keeps what the dashboard has open current: the changed files,
// and the services Containerfile.dev declares, which an agent may just have written.
func (m *Manager) refreshWatched() {
	m.mu.Lock()
	var watched []*Task
	for _, t := range m.allTasksL() {
		if t.viewers > 0 && t.info.Phase != PhasePlan {
			watched = append(watched, t)
		}
	}
	m.mu.Unlock()
	for _, t := range watched {
		t.refreshChanges()
		t.syncDeclared()
	}
}

// Shutdown stops every workspace (containers included).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	projects := m.sortedProjectsL()
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range projects {
		wg.Add(1)
		go func() { defer wg.Done(); p.close() }()
	}
	wg.Wait()
}

func (m *Manager) anyWorking() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.allTasksL() {
		if t.workingL() {
			return true
		}
	}
	return false
}

// anyAwake: work the machine must not suspend under. A task waiting out a
// usage limit counts: its timer is hours away and would not fire on a sleeping
// machine, while a restart may take it (the wait is on disk, and picked back
// up), which is why this is the inhibitor's question and not anyWorking's.
func (m *Manager) anyAwake() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.allTasksL() {
		if t.workingL() || t.info.LimitUntil > 0 {
			return true
		}
	}
	return false
}

// --- commands ---

type ref struct {
	Pid     string `json:"pid"`
	Tid     string `json:"tid"`
	Dir     string `json:"dir"`
	Name    string `json:"name"`
	Text    string `json:"text"`
	Phase   Phase  `json:"phase"`
	Message string `json:"message"`
	// What a dashboard attached to a chat message (see uploads.go).
	Files []ChatFile `json:"files"`
}

func decode(raw json.RawMessage) (ref, map[string]any, error) {
	var r ref
	partial := map[string]any{}
	if len(raw) == 0 {
		return r, partial, nil
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, partial, errors.New("bad arguments")
	}
	_ = json.Unmarshal(raw, &partial)
	delete(partial, "pid")
	delete(partial, "tid")
	return r, partial, nil
}

// Cmds returns the command handlers to register on the hub.
func (m *Manager) Cmds() map[string]hub.CmdHandler {
	withTask := func(f func(t *Task, r ref, partial map[string]any) (any, error)) hub.CmdHandler {
		return func(raw json.RawMessage) (any, error) {
			r, partial, err := decode(raw)
			if err != nil {
				return nil, err
			}
			t, err := m.task(r.Pid, r.Tid)
			if err != nil {
				return nil, err
			}
			return f(t, r, partial)
		}
	}
	withProject := func(f func(p *Project, r ref, partial map[string]any) (any, error)) hub.CmdHandler {
		return func(raw json.RawMessage) (any, error) {
			r, partial, err := decode(raw)
			if err != nil {
				return nil, err
			}
			p, err := m.project(r.Pid)
			if err != nil {
				return nil, err
			}
			return f(p, r, partial)
		}
	}
	return map[string]hub.CmdHandler{
		// Replies with the project's state as well, so the dashboard can show
		// it before the patches it was not yet listening for.
		"addProject": func(raw json.RawMessage) (any, error) {
			r, _, err := decode(raw)
			if err != nil {
				return nil, err
			}
			p, err := m.Add(r.Dir, r.Name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"pid": p.pid, "project": m.hub.Snapshot("projects", p.pid)}, nil
		},
		"removeProject": func(raw json.RawMessage) (any, error) {
			r, _, err := decode(raw)
			if err != nil {
				return nil, err
			}
			return nil, m.Remove(r.Pid)
		},
		"setProject": withProject(func(p *Project, r ref, partial map[string]any) (any, error) {
			return nil, p.SetConfig(partial)
		}),
		"openProjectCode": withProject(func(p *Project, r ref, partial map[string]any) (any, error) {
			p.bgOpenCode()
			return nil, nil
		}),
		"createTask": withProject(func(p *Project, r ref, partial map[string]any) (any, error) {
			tid, err := p.CreateTask(partial)
			return map[string]any{"tid": tid}, err
		}),
		"updateTask":     withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.Update(partial) }),
		"openTask":       withTask(func(t *Task, r ref, partial map[string]any) (any, error) { t.Open(); return nil, nil }),
		"chat":           withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.SendChat(r.Text, r.Files) }),
		"stopAgent":      withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.StopAgent() }),
		"mergeTask":      withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.Merge(r.Message) }),
		"rebaseTask":     withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.Rebase() }),
		"moveTask":       withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.MoveTo(r.Phase) }),
		"deleteTask":     withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.Delete() }),
		"runService":     withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.RunService(r.Name) }),
		"stopService":    withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.StopService(r.Name) }),
		"restartService": withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.RestartService(r.Name) }),
		"reloadTask":     withTask(func(t *Task, r ref, partial map[string]any) (any, error) { return nil, t.Reload() }),
		// Exit once idle and let the UI start the binary it wants; containers stay up and are reused.
		"restart": func(raw json.RawMessage) (any, error) {
			m.scheduleRestart()
			return nil, nil
		},
		"stop": func(raw json.RawMessage) (any, error) {
			go func() { time.Sleep(200 * time.Millisecond); m.Shutdown(); m.exit(0) }()
			return nil, nil
		},
	}
}
