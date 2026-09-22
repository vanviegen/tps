// Package ui is the browser-facing server, the dashboard: it serves the
// embedded web app, speaks the hub protocol to browsers over websockets, and
// mirrors the projects it lists from the daemons that own them (local or
// over SSH).
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/vanviegen/tps/daemon"
	"github.com/vanviegen/tps/hub"
)

type Options struct {
	Addr  string
	WebFS fs.FS // holds web/index.html and web/dist
	Open  bool  // open the browser once listening
	// DaemonBinary is pushed to remote hosts instead of this executable (for another architecture).
	DaemonBinary string
}

type UI struct {
	hub   *hub.Hub
	webFS fs.FS
	mu    sync.Mutex
	links map[string]*Link // by host id
	hosts []string         // the ssh destinations listed in dashboard.json; this machine is always shown
	order []string         // project ids as the user arranged them in the sidebar; see dashboard.go

	daemonBinary string
	askpass      *askpassServer
}

// Commands that belong to a project or task and are forwarded to its daemon,
// with the project id translated into the pid the daemon knows it by.
var daemonCmds = []string{"setProject", "removeProject", "openProjectCode", "createTask", "updateTask", "openTask", "chat", "attach", "preview", "stopAgent", "mergeTask", "rebaseTask", "moveTask", "deleteTask", "runService", "stopService", "restartService", "reloadTask", "usePoint"}

func Run(o Options) error {
	u := &UI{
		hub:   hub.New(map[string]any{"projects": map[string]any{}, "hosts": map[string]any{}, "models": daemon.FallbackModels, "projectOrder": []string{}}),
		webFS: o.WebFS, links: map[string]*Link{},
		daemonBinary: o.DaemonBinary,
	}
	var err error
	if u.askpass, err = u.startAskpass(); err != nil {
		return err
	}
	u.registerCmds()
	u.hub.OnWatch = u.onWatch
	hosts, order, legacy := loadHosts()
	u.order = order
	// This machine is always shown, and shown first; the rest are those listed
	// (and those a project of an older dashboard lived on).
	seen := map[string]bool{"": true}
	for _, l := range legacy {
		hosts = append(hosts, l.Host)
	}
	for _, dest := range hosts {
		if !seen[dest] {
			seen[dest] = true
			u.hosts = append(u.hosts, dest)
		}
	}
	u.mu.Lock()
	u.saveL()
	u.mu.Unlock()
	u.publishOrder()
	u.linkTo("", legacy)
	for _, dest := range u.hosts {
		u.linkTo(dest, legacy)
	}
	u.hub.Set([]string{"ready"}, true)
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String() + "/"
	log.Printf("TPS listening on %s", url)
	if o.Open {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		_ = exec.Command(opener, url).Start()
	}
	return http.Serve(ln, u.handler())
}

// linkTo returns the link to a host, making (and starting) one when needed.
// Names an older dashboard gave the projects there are handed over once.
func (u *UI) linkTo(dest string, legacy []legacyProject) *Link {
	u.mu.Lock()
	defer u.mu.Unlock()
	hid := hostID(dest)
	if l := u.links[hid]; l != nil {
		return l
	}
	var tr transport = localTransport{}
	if dest != "" {
		tr = newSSHTransport(u, hid, dest)
	}
	l := newLink(u, hid, dest, tr)
	for _, e := range legacy {
		if e.Host == dest && e.Name != "" {
			l.names[e.Dir] = e.Name
		}
	}
	u.links[hid] = l
	go l.run()
	return l
}

func (u *UI) link(hid string) (*Link, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if l := u.links[hid]; l != nil {
		return l, nil
	}
	return nil, fmt.Errorf("Unknown host: %s", hid)
}

// resolve takes a project apart into the host it lives on and the pid its
// daemon knows it by: "<host id>:<pid>" is all a project id is.
func (u *UI) resolve(id string) (*Link, string, error) {
	hid, pid, ok := strings.Cut(id, ":")
	if !ok {
		return nil, "", fmt.Errorf("Unknown project: %s", id)
	}
	l, err := u.link(hid)
	if err != nil {
		return nil, "", err
	}
	return l, pid, nil
}

func (u *UI) onWatch(key string, count int) {
	id, tid, _ := strings.Cut(key, "/")
	if l, pid, err := u.resolve(id); err == nil {
		l.watch(pid+"/"+tid, count > 0)
	}
}

func (u *UI) registerCmds() {
	for _, name := range daemonCmds {
		u.hub.Cmds[name] = func(raw json.RawMessage) (any, error) {
			args := map[string]any{}
			_ = json.Unmarshal(raw, &args)
			id, _ := args["pid"].(string)
			l, pid, err := u.resolve(id)
			if err != nil {
				return nil, err
			}
			args["pid"] = pid
			return l.cmd(name, args)
		}
	}
	u.hub.Cmds["addProject"] = u.addProject
	u.hub.Cmds["addHost"] = u.addHost
	u.hub.Cmds["setHost"] = u.setHost
	u.hub.Cmds["removeHost"] = u.removeHost
	u.hub.Cmds["connectHost"] = u.connectHost
	u.hub.Cmds["login"] = u.login
	u.hub.Cmds["stopDaemon"] = u.stopDaemon
	u.hub.Cmds["updateDaemon"] = u.updateDaemon
	u.hub.Cmds["answer"] = u.answer
	u.hub.Cmds["setProjectOrder"] = u.setProjectOrder
}

// --- http ---

func (u *UI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", u.serveWS)
	mux.HandleFunc("/code/", u.serveCode)
	mux.HandleFunc("/", u.serveStatic)
	return mux
}

func (u *UI) serveWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(16 << 20)
	client := u.hub.AddClient()
	ctx := context.Background()
	go func() {
		for raw := range client.Out {
			if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
				break
			}
		}
		c.Close(websocket.StatusNormalClosure, "")
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		u.hub.Handle(client, data)
	}
	u.hub.RemoveClient(client)
}

var codeRoute = regexp.MustCompile(`^/code/([^/]+)/([^/]+)(/.*)?$`)

// serveCode proxies /code/<pid>/<tid>/... to the task's code-server, and
// /code/<pid>/-/... to the one on the project's own checkout. Task ids are
// numbers, so "-" can never be one.
func (u *UI) serveCode(w http.ResponseWriter, r *http.Request) {
	m := codeRoute.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	l, _, err := u.resolve(m[1])
	portPath := []string{"projects", m[1], "tasks", m[2], "codePort"}
	if m[2] == "-" {
		portPath = []string{"projects", m[1], "codePort"}
	}
	port, _ := u.hub.Get(portPath...).(float64)
	if err != nil || port == 0 {
		w.Header().Set("content-type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<meta http-equiv="refresh" content="2"><body style="font-family:sans-serif;color:#888">Workspace is not running&hellip;</body>`))
		return
	}
	p := m[3]
	if p == "" {
		p = "/"
	}
	ctx := context.WithValue(r.Context(), proxyKey{}, proxyTarget{port: int(port), path: p})
	l.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// The files that keep their own name outside /dist/: the manifest and the
// icons a browser installs the dashboard as an app from.
var appFiles = map[string]bool{
	"/manifest.webmanifest": true,
	"/icon.svg":             true, "/icon-192.png": true, "/icon-512.png": true,
}

func (u *UI) serveStatic(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.Contains(p, "..") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(p, "/dist/") && !appFiles[p] {
		p = "/index.html" // every app path, file names in it included, gets the SPA
	}
	data, err := fs.ReadFile(u.webFS, "web"+p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(p))
	switch path.Ext(p) {
	case ".map":
		ctype = "application/json"
	case ".webmanifest":
		ctype = "application/manifest+json"
	}
	// The shell is how a running app hears of a new build: it is checked with
	// the server every time rather than remembered.
	if p == "/index.html" {
		w.Header().Set("cache-control", "no-cache")
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("content-type", ctype)
	_, _ = w.Write(data)
}
