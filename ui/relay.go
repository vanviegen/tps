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
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vanviegen/tps/hub"
)

// A Link is the dashboard's connection to one daemon. It mirrors the daemon's
// projects into the state tree under "<host id>:<pid>", forwards commands and
// watches the other way, and for remote hosts turns the ports tasks publish
// into local listeners so the browser can open them.
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
	mirrored map[string]bool             // the daemon's pids that are in the state tree
	watches  map[string]bool             // keys (pid/tid) watched at the daemon
	forwards map[string]map[int]*forward // task key → port on the host → local listener piped to it
	wake     chan struct{}
	closed   bool
	stopped  bool              // the user stopped the daemon: don't start it again until asked
	names    map[string]string // dir → name, from an older dashboard; handed to the daemon once

	// What the host shows: the connection state plus what the daemon reported.
	status, errText string
	ask             map[string]any // the question ssh raised on the way here (see askpass.go)
	askKey          string         // what its answer is remembered as
	build           string         // the daemon's build id
	homeDir         string         // the daemon user's home there, for showing paths as ~/…
	protocol        int
	restarting      bool
	models          []any          // the models the agents on this host offer
	modelsError     string         // why they are the built-in fallback instead
	agentPrompt     string         // the rules its daemon puts above every agent conversation
	login           string         // what its claude login needs, if anything (see daemon/login.go)
	signin          string         // how a sign-in for it here is going, or failed (see login.go)
	signinURL       string         // the page it is made on, while it can be
	signinCode      io.WriteCloser // where the code that page shows goes, while a sign-in is under way
	autoUpgraded    bool           // this UI already asked the daemon to restart into its build
}

type replyFn func(result json.RawMessage, err error)

// A forward is one local listener, piping each connection to a port on the
// host's loopback (through the transport, so ssh -W for a remote host).
type forward struct {
	remotePort, localPort int
	ln                    net.Listener
}

func (l *Link) listen(remotePort int) (*forward, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &forward{remotePort: remotePort, localPort: ln.Addr().(*net.TCPAddr).Port, ln: ln}
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
	return f, nil
}

type proxyKey struct{}

type proxyTarget struct {
	port int
	path string
}

func newLink(u *UI, hid, dest string, tr transport) *Link {
	l := &Link{ui: u, hid: hid, dest: dest, remote: dest != "", tr: tr,
		pending: map[int]replyFn{}, mirrored: map[string]bool{}, watches: map[string]bool{}, forwards: map[string]map[int]*forward{},
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
	host := map[string]any{"name": l.name(), "dest": l.dest, "status": l.status, "error": l.errText, "warning": warning, "home": l.homeDir}
	if l.ask != nil {
		host["ask"] = l.ask
	}
	if l.models != nil {
		host["models"] = l.models
	}
	if l.modelsError != "" {
		host["modelsError"] = l.modelsError
	}
	if l.agentPrompt != "" {
		host["agentPrompt"] = l.agentPrompt
	}
	if l.login != "" {
		host["login"] = l.login
	}
	if l.signin != "" {
		host["signin"] = l.signin
	}
	if l.signinCode != nil {
		host["signingIn"] = true
		host["signinURL"] = l.signinURL
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
		stopped, asking := l.stopped, l.ask != nil
		l.mu.Unlock()
		// Stopped by the user, or waiting for an answer to what ssh asked:
		// either way the host sits still and says so until Wake comes round.
		if stopped {
			l.setStatus("stopped", "")
			<-l.wake
			continue
		}
		if asking {
			l.setStatus("asking", "")
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
		// We are in: a prompt ssh raised on the way (and we turned down) was
		// one it had another answer to, so there is nothing left to ask.
		l.mu.Lock()
		l.ask, l.askKey = nil, ""
		l.mu.Unlock()
		l.serve(conn)
		if !l.isClosed() {
			l.setStatus("disconnected", "connection lost")
			// What the host said is only known while it is connected.
			l.dropAll()
		}
	}
}

// Wake retries the link right away, whatever it was waiting for.
func (l *Link) Wake() {
	l.mu.Lock()
	l.stopped, l.ask, l.askKey = false, nil, ""
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
		status, errText, asking := l.status, l.errText, l.ask != nil
		l.mu.Unlock()
		// A dial that ended in a question for the user is not a failed
		// attempt: it waits for the answer, and so do we, until the deadline.
		if asking {
			continue
		}
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
	for key := range l.forwards {
		l.closeForwardsL(key)
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

// dropAll takes the host's projects off the board.
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
	l.modelsError, _ = state["modelsError"].(string)
	l.agentPrompt, _ = state["agentPrompt"].(string)
	l.login, _ = state["login"].(string)
	l.homeDir, _ = state["home"].(string)
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
		l.mirror(pid, project)
	}
	l.renewWatches()
	l.handOverIdentity()
	l.handOverNames(projects)
	l.setStatus("connected", "")
	// A daemon of another build reports stale state (the model list included)
	// until someone presses "Update daemon"; do it for them, once per host per
	// UI run. It is what the button does: install this binary and have the
	// daemon exit as soon as nothing is running, which the board says it will.
	// Too old a protocol is upgraded above, remote or not, for the same reason.
	l.mu.Lock()
	outdated := !l.autoUpgraded && !l.restarting && l.build != BuildID()
	l.autoUpgraded = l.autoUpgraded || outdated
	l.mu.Unlock()
	if outdated {
		go func() { _ = l.upgrade() }()
	}
}

// handOverIdentity gives the daemon this machine's git identity, so that the
// commits it makes are by whoever steers it from here rather than by TPS
// itself. A machine that has none leaves the host the identity it has.
func (l *Link) handOverIdentity() {
	name, email := gitIdentity()
	if name == "" || email == "" {
		return
	}
	l.call("setIdentity", map[string]any{"name": name, "email": email}, func(_ json.RawMessage, err error) {
		if err != nil {
			log.Printf("host %s: handing over the git identity failed: %v", l.hid, err)
		}
	})
}

// gitIdentity is who a commit made on this machine would be authored by. It is
// git's own answer — its config, the GIT_AUTHOR_* environment, and the account
// and host it falls back on — as "Name <email> <timestamp>". Nothing comes back
// when git is not installed, or when it would have to make an identity up: it
// refuses to, and so do we.
func gitIdentity() (string, string) {
	cmd := exec.Command("git", "var", "GIT_AUTHOR_IDENT")
	cmd.Dir, _ = os.UserHomeDir() // the machine's identity, not that of a repository tps was started in
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	name, rest, _ := strings.Cut(string(out), " <")
	email, _, ok := strings.Cut(rest, ">")
	if !ok {
		return "", ""
	}
	return name, email
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
	// Top-level daemon state the host card shows.
	if len(p) == 1 {
		l.mu.Lock()
		switch p[0] {
		case "models":
			l.models, _ = value.([]any)
		case "modelsError":
			l.modelsError, _ = value.(string)
		case "login":
			l.login, _ = value.(string)
		case "restarting":
			l.restarting, _ = value.(bool)
		default:
			l.mu.Unlock()
			return
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
		switch {
		case len(p) == 3 && p[2] == "tasks":
			l.closeForwards(pid + "/")
		case p[2] == "tasks" && (len(p) == 4 || len(p) == 5 && p[4] == "ports"):
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
	case len(p) == 5 && p[2] == "tasks" && p[4] == "ports":
		value = l.forwardPorts(pid+"/"+p[3], value)
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
	if ports, ok := task["ports"]; ok {
		task["ports"] = l.forwardPorts(key, ports)
	} else {
		l.closeForwards(key)
	}
	return task
}

// forwardPorts maps a remote task's published ports — [{port, host, live}],
// host being a loopback port there — to local listeners piped to them, one
// per host port, kept across rounds and dropped when the port is gone. The
// browser then sees the local port in "host".
func (l *Link) forwardPorts(key string, v any) any {
	if !l.remote {
		return v
	}
	list, _ := v.([]any)
	l.mu.Lock()
	defer l.mu.Unlock()
	had := l.forwards[key]
	kept := map[int]*forward{}
	var out []any
	for _, entry := range list {
		m, _ := entry.(map[string]any)
		host, _ := m["host"].(float64)
		if host == 0 {
			continue
		}
		f := had[int(host)]
		if f == nil {
			var err error
			if f, err = l.listen(int(host)); err != nil {
				continue
			}
		}
		kept[int(host)] = f
		m["host"] = f.localPort
		out = append(out, m)
	}
	for port, f := range had {
		if kept[port] == nil {
			f.ln.Close()
		}
	}
	if len(kept) == 0 {
		delete(l.forwards, key)
		return nil
	}
	l.forwards[key] = kept
	return out
}

// closeForwards drops the listeners of the task key, or of every key with the prefix.
func (l *Link) closeForwards(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.forwards {
		if k == key || strings.HasSuffix(key, "/") && strings.HasPrefix(k, key) {
			l.closeForwardsL(k)
		}
	}
}

func (l *Link) closeForwardsL(key string) {
	for _, f := range l.forwards[key] {
		f.ln.Close()
	}
	delete(l.forwards, key)
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
