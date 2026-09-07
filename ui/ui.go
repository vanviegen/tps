// Package ui is the browser-facing server: it serves the embedded web app,
// speaks the hub protocol to browsers over websockets, and relays every
// project to the daemon that owns it (local or over SSH).
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
	"github.com/vanviegen/agent-manager/daemon"
	"github.com/vanviegen/agent-manager/hub"
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
	links map[string]*Link

	daemonBinary string
	askMu        sync.Mutex
	askNext      int
	asks         map[int]chan answer
}

// Commands that belong to a project or task and are forwarded to its daemon.
var daemonCmds = []string{"removeProject", "setProject", "createContainerfile", "createTask", "updateTask",
	"openTask", "assignTask", "chat", "stopAgent", "mergeTask", "moveTask", "deleteTask"}

func Run(o Options) error {
	u := &UI{
		hub: hub.New(map[string]any{"projects": map[string]any{}, "hosts": map[string]any{},
			"models": daemon.Models, "tools": daemon.ToolOptions}),
		webFS: o.WebFS, links: map[string]*Link{},
		daemonBinary: o.DaemonBinary, asks: map[int]chan answer{},
	}
	u.registerCmds()
	u.hub.OnWatch = u.onWatch
	u.addLink("local", "", localTransport{})
	for _, h := range loadHosts() {
		u.addHostLink(h)
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

func (u *UI) addLink(hid, dest string, tr transport) *Link {
	l := newLink(u, hid, dest, tr)
	u.mu.Lock()
	u.links[hid] = l
	u.mu.Unlock()
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

// splitPid takes a composite project id apart: host id and the daemon's pid.
func splitPid(cpid string) (hid, pid string) {
	hid, pid, ok := strings.Cut(cpid, "~")
	if !ok {
		return "local", cpid
	}
	return hid, pid
}

func (u *UI) onWatch(key string, count int) {
	cpid, tid, _ := strings.Cut(key, "/")
	hid, pid := splitPid(cpid)
	if l, err := u.link(hid); err == nil {
		l.watch(pid+"/"+tid, count > 0)
	}
}

func (u *UI) registerCmds() {
	for _, name := range daemonCmds {
		u.hub.Cmds[name] = func(raw json.RawMessage) (any, error) {
			args := map[string]any{}
			_ = json.Unmarshal(raw, &args)
			cpid, _ := args["pid"].(string)
			hid, pid := splitPid(cpid)
			l, err := u.link(hid)
			if err != nil {
				return nil, err
			}
			args["pid"] = pid
			return l.cmd(name, args)
		}
	}
	u.hub.Cmds["addProject"] = func(raw json.RawMessage) (any, error) {
		args := map[string]any{}
		_ = json.Unmarshal(raw, &args)
		hid, _ := args["host"].(string)
		if hid == "" {
			hid = "local"
		}
		delete(args, "host")
		l, err := u.link(hid)
		if err != nil {
			return nil, err
		}
		res, err := l.cmd("addProject", args)
		if err != nil {
			return nil, err
		}
		var out struct {
			Pid string `json:"pid"`
		}
		_ = json.Unmarshal(res, &out)
		return map[string]any{"pid": l.cpid(out.Pid)}, nil
	}
	u.hub.Cmds["connectHost"] = func(raw json.RawMessage) (any, error) {
		var args struct {
			Hid string `json:"hid"`
		}
		_ = json.Unmarshal(raw, &args)
		l, err := u.link(args.Hid)
		if err != nil {
			return nil, err
		}
		l.Wake()
		return nil, nil
	}
	u.hub.Cmds["addHost"] = u.addHost
	u.hub.Cmds["removeHost"] = u.removeHost
	u.hub.Cmds["copyCredentials"] = u.copyCredentials
	u.hub.Cmds["stopDaemon"] = u.stopDaemon
	u.hub.Cmds["answer"] = u.answer
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

// serveCode proxies /code/<pid>/<tid>/... to the task's code-server.
func (u *UI) serveCode(w http.ResponseWriter, r *http.Request) {
	m := codeRoute.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	hid, _ := splitPid(m[1])
	l, err := u.link(hid)
	port, _ := u.hub.Get("projects", m[1], "tasks", m[2], "codePort").(float64)
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

func (u *UI) serveStatic(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.Contains(p, "..") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if p == "/" || path.Ext(p) == "" {
		p = "/index.html" // SPA fallback
	}
	data, err := fs.ReadFile(u.webFS, "web"+p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(p))
	if path.Ext(p) == ".map" {
		ctype = "application/json"
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("content-type", ctype)
	_, _ = w.Write(data)
}
