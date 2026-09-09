package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vanviegen/agent-manager/hub"
)

// A Link is the dashboard's connection to one daemon. It mirrors the daemon's
// projects into the state tree under "<host id>:<pid>", forwards commands and
// watches the other way, and for remote hosts turns app ports into local
// listeners so the browser can open them.
type Link struct {
	ui     *UI
	hid    string
	dest   string // "" for the local daemon
	remote bool
	tr     transport
	proxy  *httputil.ReverseProxy

	mu       sync.Mutex
	conn     net.Conn
	w        *bufio.Writer
	nextID   int
	pending  map[int]replyFn
	mirrored map[string]bool     // the daemon's pids that are in the state tree
	watches  map[string]bool     // keys (pid/tid) watched at the daemon
	forwards map[string]*forward // task key → local listener for its app port
	wake     chan struct{}
	closed   bool
	stopped  bool              // the user stopped the daemon: don't start it again until asked
	names    map[string]string // dir → name, from an older dashboard; handed to the daemon once

	// What the host shows: the connection state plus what the daemon reported.
	status, errText string
	build           string // the daemon's build id
	protocol        int
	restarting      bool
	models          []any // the models this host's claude offers
}

type replyFn func(result json.RawMessage, err error)

type forward struct {
	remotePort, localPort int
	ln                    net.Listener
}

type proxyKey struct{}

type proxyTarget struct {
	port int
	path string
}

func newLink(u *UI, hid, dest string, tr transport) *Link {
	l := &Link{ui: u, hid: hid, dest: dest, remote: dest != "", tr: tr,
		pending: map[int]replyFn{}, mirrored: map[string]bool{}, watches: map[string]bool{}, forwards: map[string]*forward{},
		names: map[string]string{}, wake: make(chan struct{}, 1)}
	l.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			t := pr.In.Context().Value(proxyKey{}).(proxyTarget)
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", fmt.Sprintf("127.0.0.1:%d", t.port)
			pr.Out.URL.Path, pr.Out.URL.RawPath = t.path, ""
			// The Host header is forwarded unchanged: code-server requires it
			// to match the browser's Origin, or it rejects websocket upgrades.
			pr.Out.Host = pr.In.Host
		},
		Transport: &http.Transport{
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				_, portStr, _ := net.SplitHostPort(addr)
				port, _ := strconv.Atoi(portStr)
				return l.tr.DialPort(port)
			},
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "TPS: workspace upstream unavailable")
		},
	}
	return l
}

// name is the host as shown to the user: without the ssh options in front.
func (l *Link) name() string {
	if fields := strings.Fields(l.dest); len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return "this machine"
}

func (l *Link) setStatus(status, errText string) {
	l.mu.Lock()
	l.status, l.errText = status, errText
	l.mu.Unlock()
	l.publishHost()
}

func (l *Link) publishHost() {
	warning := ""
	if w, ok := l.tr.(interface{ Warning() string }); ok {
		warning = w.Warning()
	}
	l.mu.Lock()
	host := map[string]any{"name": l.name(), "dest": l.dest, "status": l.status, "error": l.errText, "warning": warning}
	if l.models != nil {
		host["models"] = l.models
	}
	if l.status == "connected" {
		host["updatable"] = l.build != BuildID()
		host["restarting"] = l.restarting
	}
	l.mu.Unlock()
	l.ui.hub.Set([]string{"hosts", l.hid}, host)
}

// close stops the link for good.
func (l *Link) close() {
	l.mu.Lock()
	l.closed = true
	conn := l.conn
	l.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	l.Wake()
	if c, ok := l.tr.(interface{ Close() }); ok {
		c.Close()
	}
}

func (l *Link) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// run keeps the link connected: dial, serve until the connection drops, back off, repeat.
func (l *Link) run() {
	backoff := 2 * time.Second
	for !l.isClosed() {
		l.mu.Lock()
		stopped := l.stopped
		l.mu.Unlock()
		if stopped {
			l.setStatus("stopped", "")
			<-l.wake
			continue
		}
		l.setStatus("connecting", "")
		conn, err := l.tr.DialDaemon()
		if err != nil {
			log.Printf("host %s: %v", l.hid, err)
			l.setStatus("disconnected", err.Error())
			select {
			case <-time.After(backoff):
			case <-l.wake:
			}
			backoff = min(backoff*2, 60*time.Second)
			continue
		}
		backoff = 2 * time.Second
		l.serve(conn)
		if !l.isClosed() {
			l.setStatus("disconnected", "connection lost")
		}
	}
}

// Wake retries a disconnected (or stopped) link right away.
func (l *Link) Wake() {
	l.mu.Lock()
	l.stopped = false
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// awaitConnection waits for the link to be connected, or for its current
// attempt to have failed.
func (l *Link) awaitConnection(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		l.mu.Lock()
		status, errText := l.status, l.errText
		l.mu.Unlock()
		switch status {
		case "connected":
			return nil
		case "disconnected", "incompatible":
			if errText != "" {
				return errors.New(errText)
			}
		}
	}
	return errors.New("connecting took too long")
}

func (l *Link) serve(conn net.Conn) {
	l.mu.Lock()
	l.conn, l.w = conn, bufio.NewWriter(conn)
	l.mu.Unlock()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		l.onMessage(sc.Bytes())
	}
	conn.Close()
	l.mu.Lock()
	l.conn, l.w = nil, nil
	pending := l.pending
	l.pending = map[int]replyFn{}
	l.watches = map[string]bool{}
	for key, f := range l.forwards {
		f.ln.Close()
		delete(l.forwards, key)
	}
	l.mu.Unlock()
	for _, cb := range pending {
		cb(nil, errors.New("host disconnected"))
	}
}

type inbound struct {
	Hello  map[string]any    `json:"hello"`
	P      []any             `json:"p"`
	V      any               `json:"v"`
	Del    bool              `json:"del"`
	C      *string           `json:"c"`
	E      json.RawMessage   `json:"e"`
	Es     []json.RawMessage `json:"es"`
	U      bool              `json:"u"`
	Re     *int              `json:"re"`
	Result json.RawMessage   `json:"result"`
	Error  string            `json:"error"`
}

func (l *Link) onMessage(raw []byte) {
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	switch {
	case msg.Hello != nil:
		l.onHello(msg.Hello)
	case msg.P != nil:
		l.onPatch(msg.P, msg.V, msg.Del)
	case msg.C != nil:
		l.onChat(*msg.C, msg)
	case msg.Re != nil:
		l.mu.Lock()
		cb := l.pending[*msg.Re]
		delete(l.pending, *msg.Re)
		l.mu.Unlock()
		if cb == nil {
			return
		}
		if msg.Error != "" {
			cb(nil, errors.New(msg.Error))
		} else {
			cb(msg.Result, nil)
		}
	}
}

// --- mirroring the daemon's projects ---

// id is what a project of this host is called in the state tree and in urls.
func (l *Link) id(pid string) string { return l.hid + ":" + pid }

// mirror puts one of the daemon's projects in the state tree.
func (l *Link) mirror(pid string, project map[string]any) {
	l.mu.Lock()
	l.mirrored[pid] = true
	l.mu.Unlock()
	l.ui.hub.Set([]string{"projects", l.id(pid)}, l.translateProject(pid, project))
}

// drop takes a project out of the tree, with the port forwards of its tasks.
func (l *Link) drop(pid string) {
	l.mu.Lock()
	delete(l.mirrored, pid)
	l.mu.Unlock()
	l.closeForwards(pid + "/")
	l.ui.hub.Set([]string{"projects", l.id(pid)}, nil)
}

// dropAll takes the host's projects off the board, for a host being removed.
func (l *Link) dropAll() {
	l.mu.Lock()
	pids := make([]string, 0, len(l.mirrored))
	for pid := range l.mirrored {
		pids = append(pids, pid)
	}
	l.mu.Unlock()
	for _, pid := range pids {
		l.drop(pid)
	}
}

// Watches don't survive a reconnect at the daemon; renew them from what the
// browsers watch.
func (l *Link) renewWatches() {
	for _, key := range l.ui.hub.WatchedKeys() {
		id, tid, _ := strings.Cut(key, "/")
		if hid, pid, ok := strings.Cut(id, ":"); ok && hid == l.hid {
			l.watch(pid+"/"+tid, true)
		}
	}
}

// onHello mirrors the daemon's projects: they are the host's list, so this
// dashboard shows them all, and only them.
func (l *Link) onHello(state map[string]any) {
	l.mu.Lock()
	l.build, _ = state["build"].(string)
	proto, _ := state["protocol"].(float64)
	l.protocol = int(proto)
	l.restarting, _ = state["restarting"].(bool)
	l.models, _ = state["models"].([]any)
	stale := map[string]bool{}
	for pid := range l.mirrored {
		stale[pid] = true
	}
	l.mu.Unlock()
	switch {
	case l.protocol > hub.Protocol:
		// Never touch a newer daemon; it is this TPS that needs updating.
		l.setStatus("incompatible", "this host runs a newer TPS; update this one")
		return
	case l.protocol < hub.MinProtocol:
		l.setStatus("updating", "")
		go l.upgrade()
		return
	}
	projects, _ := state["projects"].(map[string]any)
	for pid, v := range projects {
		project, _ := v.(map[string]any)
		delete(stale, pid)
		l.mirror(pid, project)
	}
	for pid := range stale { // gone from the host while we were away
		l.drop(pid)
	}
	l.renewWatches()
	l.handOverNames(projects)
	l.setStatus("connected", "")
}

// handOverNames gives the daemon the names an older dashboard kept for its
// projects, once, so upgrading does not rename them all to their directory.
func (l *Link) handOverNames(projects map[string]any) {
	l.mu.Lock()
	names := l.names
	l.names = map[string]string{}
	l.mu.Unlock()
	if len(names) == 0 {
		return
	}
	for pid, v := range projects {
		project, _ := v.(map[string]any)
		dir, _ := project["dir"].(string)
		name := names[dir]
		if name == "" || name == project["name"] {
			continue
		}
		l.call("setProject", map[string]any{"pid": pid, "name": name}, func(json.RawMessage, error) {})
	}
}

func (l *Link) onPatch(path []any, value any, del bool) {
	p := make([]string, len(path))
	for i, seg := range path {
		p[i] = fmt.Sprint(seg)
	}
	if len(p) == 1 && (p[0] == "restarting" || p[0] == "models") {
		l.mu.Lock()
		if p[0] == "models" {
			l.models, _ = value.([]any)
		} else {
			l.restarting, _ = value.(bool)
		}
		l.mu.Unlock()
		l.publishHost()
		return
	}
	if len(p) < 2 || p[0] != "projects" {
		return
	}
	pid := p[1]
	out := append([]string{"projects", l.id(pid)}, p[2:]...)
	if del {
		if len(p) == 2 { // the project is gone from the host, so from here too
			l.drop(pid)
			return
		}
		l.ui.hub.Set(out, nil)
		if len(p) == 4 && p[2] == "tasks" {
			l.closeForwards(pid + "/" + p[3])
		} else if len(p) == 5 && p[4] == "appPort" {
			l.closeForwards(pid + "/" + p[3])
		}
		return
	}
	switch {
	case len(p) == 2:
		project, _ := value.(map[string]any)
		l.mirror(pid, project)
		return
	case len(p) == 4 && p[2] == "tasks":
		value = l.translateTask(pid+"/"+p[3], value)
	case len(p) == 5 && p[2] == "tasks" && p[4] == "appPort":
		value = l.forwardPort(pid+"/"+p[3], value)
	}
	l.ui.hub.Set(out, value)
}

// translateProject adds the one thing the daemon cannot know to its project
// state: which host this is.
func (l *Link) translateProject(pid string, project map[string]any) map[string]any {
	if project == nil {
		project = map[string]any{}
	}
	project["host"] = l.hid
	if tasks, ok := project["tasks"].(map[string]any); ok {
		for tid, t := range tasks {
			tasks[tid] = l.translateTask(pid+"/"+tid, t)
		}
	}
	return project
}

func (l *Link) translateTask(key string, v any) any {
	task, ok := v.(map[string]any)
	if !ok || !l.remote {
		return v
	}
	if port, ok := task["appPort"]; ok {
		task["appPort"] = l.forwardPort(key, port)
	} else {
		l.closeForwards(key)
	}
	return task
}

// forwardPort maps a remote task's app port to a local listener piped to it.
func (l *Link) forwardPort(key string, v any) any {
	if !l.remote {
		return v
	}
	port, _ := v.(float64)
	if port == 0 {
		l.closeForwards(key)
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.forwards[key]; f != nil {
		if f.remotePort == int(port) {
			return f.localPort
		}
		f.ln.Close()
		delete(l.forwards, key)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil
	}
	f := &forward{remotePort: int(port), localPort: ln.Addr().(*net.TCPAddr).Port, ln: ln}
	l.forwards[key] = f
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				up, err := l.tr.DialPort(f.remotePort)
				if err != nil {
					c.Close()
					return
				}
				go func() { _, _ = io.Copy(up, c); up.Close() }()
				_, _ = io.Copy(c, up)
				c.Close()
			}()
		}
	}()
	return f.localPort
}

// closeForwards drops the listeners of the task key, or of every key with the prefix.
func (l *Link) closeForwards(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, f := range l.forwards {
		if k == key || strings.HasPrefix(k, key) && strings.HasSuffix(key, "/") {
			f.ln.Close()
			delete(l.forwards, k)
		}
	}
}

func entryID(raw json.RawMessage) string {
	var head struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &head)
	return head.ID
}

func (l *Link) onChat(key string, msg inbound) {
	pid, tid, _ := strings.Cut(key, "/")
	ckey := l.id(pid) + "/" + tid
	switch {
	case msg.Es != nil:
		entries := make([]hub.Entry, len(msg.Es))
		for i, raw := range msg.Es {
			entries[i] = hub.Entry{ID: entryID(raw), V: raw}
		}
		l.ui.hub.SetChat(ckey, entries)
	case msg.U:
		l.ui.hub.ChatUpdate(ckey, hub.Entry{ID: entryID(msg.E), V: msg.E})
	default:
		l.ui.hub.Chat(ckey, hub.Entry{ID: entryID(msg.E), V: msg.E})
	}
}

func (l *Link) send(msg any) error {
	raw, _ := json.Marshal(msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return errors.New("host disconnected")
	}
	if _, err := l.w.Write(append(raw, '\n')); err != nil {
		return err
	}
	return l.w.Flush()
}

func (l *Link) watch(key string, on bool) {
	l.mu.Lock()
	if l.watches[key] == on {
		l.mu.Unlock()
		return
	}
	if on {
		l.watches[key] = true
	} else {
		delete(l.watches, key)
	}
	l.mu.Unlock()
	// A watch that could not be sent must not be remembered as placed, or the
	// renewal after connecting would skip it.
	if err := l.send(map[string]any{"watch": key, "on": on}); err != nil && on {
		l.mu.Lock()
		delete(l.watches, key)
		l.mu.Unlock()
	}
}

// call sends a command to the daemon; cb gets the reply, from the message
// loop, or the reason there will be none.
func (l *Link) call(name string, args any, cb replyFn) {
	l.mu.Lock()
	if l.conn == nil || l.status == "incompatible" {
		status := l.status
		l.mu.Unlock()
		cb(nil, fmt.Errorf("%s is %s", l.name(), status))
		return
	}
	l.nextID++
	id := l.nextID
	l.pending[id] = cb
	l.mu.Unlock()
	if err := l.send(map[string]any{"id": id, "cmd": name, "args": args}); err != nil {
		l.mu.Lock()
		delete(l.pending, id)
		l.mu.Unlock()
		cb(nil, err)
	}
}

type reply struct {
	result json.RawMessage
	err    error
}

// cmd forwards a command to the daemon and waits for its reply.
func (l *Link) cmd(name string, args any) (json.RawMessage, error) {
	ch := make(chan reply, 1)
	l.call(name, args, func(result json.RawMessage, err error) { ch <- reply{result, err} })
	select {
	case r := <-ch:
		return r.result, r.err
	case <-time.After(10 * time.Minute):
		return nil, errors.New("the daemon did not answer")
	}
}

// upgrade installs the binary this UI runs on the host (for a remote one)
// and asks the daemon to restart into it once nothing is running.
func (l *Link) upgrade() error {
	if p, ok := l.tr.(pusher); ok {
		if err := p.Push(); err != nil {
			log.Printf("host %s: installing the new binary failed: %v", l.hid, err)
			l.setStatus(l.status, err.Error())
			return err
		}
	}
	if _, err := l.cmd("restart", map[string]any{}); err != nil {
		return err
	}
	log.Printf("host %s: daemon restarts into build %s when idle", l.hid, BuildID()[:12])
	return nil
}

// pusher is implemented by transports that can install our binary on their host.
type pusher interface {
	Push() error
}
