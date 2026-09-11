// Package podnester serves a container a Docker-compatible API socket of its
// own, backed by the host's podman, on which everything it makes is a
// sibling container labeled as its own and hidden from everyone else's view.
// The top-level container can run and compose services, mount its own files
// into them and publish their ports to itself, and cannot reach anything
// beyond what it was given: no other container, no host path it does not
// already see, no privilege podman would not grant it.
//
// The proxy speaks the API in front of podman's own socket (`podman system
// service`), checking each request against an allowlist: unknown endpoints
// and unknown fields of a container spec are refused rather than passed on,
// so a future option of the API cannot open a hole by being unknown here.
package podnester

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config says whose socket a Proxy serves and how its siblings are to run.
type Config struct {
	// Upstream is podman's own API socket, see EnsureService.
	Upstream string
	// Owner is the name of the top-level container: the one the socket is
	// served to. Everything made through the proxy is labeled with it.
	Owner string
	// Label is the key of that label. Default "podnester.owner".
	Label string
	// Prefix goes in front of every container, volume and network name a
	// client gives, so tasks sharing a host cannot collide, and comes off
	// again in every answer. Default Owner + "-".
	Prefix string
	// SecurityOpt entries are added to every sibling. (Siblings need no user
	// namespace setting: they join the owner.s, see createCtx.usernsMode.) "label=disable" is
	// what an owner running with that needs, so the files it shares stay
	// readable without relabeling.
	SecurityOpt []string
	// Control is a host directory shared with the owner, mounted there at
	// ControlMount: it holds the socket and the forwarders' control files.
	Control      string
	ControlMount string
	// Forwarder is the command that runs a port forwarder inside the owner
	// (see Forward), as an argv prefix; "--control FILE LISTEN TARGET" is
	// appended. Default: a copy of this binary placed in Control/bin, so the
	// binary embedding the proxy must dispatch Subcommand and be static.
	Forwarder []string
	// Logf gets the proxy's notes; nil for log.Printf.
	Logf func(format string, args ...any)
}

// portsLabel records the port bindings a sibling asked for.
const portsLabel = "podnester.ports"

// Proxy is one owner's socket: an http.Handler filtering the API.
type Proxy struct {
	cfg Config
	up  *upstream
	rp  *httputil.ReverseProxy

	fwdMu sync.Mutex
	fwds  map[string]map[string]*forwarder // container id → listen address → forwarder
}

// New makes a Proxy. Nothing is served until Serve.
func New(cfg Config) (*Proxy, error) {
	if cfg.Upstream == "" || cfg.Owner == "" || cfg.Control == "" || cfg.ControlMount == "" {
		return nil, errors.New("podnester: Upstream, Owner, Control and ControlMount are required")
	}
	if cfg.Label == "" {
		cfg.Label = "podnester.owner"
	}
	if cfg.Prefix == "" {
		cfg.Prefix = cfg.Owner + "-"
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if err := os.MkdirAll(cfg.Control, 0o777); err != nil {
		return nil, err
	}
	_ = os.Chmod(cfg.Control, 0o777) // the owner's user may be any uid
	exe, exeErr := os.Executable()
	if cfg.Forwarder == nil {
		if exeErr != nil {
			return nil, exeErr
		}
		if err := installBinary(exe, filepath.Join(cfg.Control, "bin", "podnester")); err != nil {
			return nil, err
		}
		cfg.Forwarder = []string{cfg.ControlMount + "/bin/podnester", "podnester-forward"}
	}
	p := &Proxy{cfg: cfg, up: newUpstream(cfg.Upstream), fwds: map[string]map[string]*forwarder{}}
	p.rp = &httputil.ReverseProxy{
		Transport:      p.up.rt,
		FlushInterval:  -1,
		Rewrite:        func(pr *httputil.ProxyRequest) { pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "podman" },
		ModifyResponse: p.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, http.StatusBadGateway, "podnester: podman is not answering: "+err.Error())
		},
	}
	return p, nil
}

// installBinary copies src to dst if dst differs, for the owner to run.
func installBinary(src, dst string) error {
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	if di, err := os.Stat(dst); err == nil && di.Size() == si.Size() && !di.ModTime().Before(si.ModTime()) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Network is the name of the owner's network on the host: what siblings
// join by default, and what the owner itself must be on for names to
// resolve. Clients know it as "bridge", like docker's default.
func (p *Proxy) Network() string { return p.cfg.Prefix + "bridge" }

// Socket is the path of the socket ListenAndServe serves, on the host.
func (p *Proxy) Socket() string { return filepath.Join(p.cfg.Control, "podman.sock") }

// SocketMount is that socket's path inside the owner: what to put in
// DOCKER_HOST and CONTAINER_HOST (as unix://...).
func (p *Proxy) SocketMount() string { return p.cfg.ControlMount + "/podman.sock" }

func (p *Proxy) ownerLabel() string         { return p.cfg.Label + "=" + p.cfg.Owner }
func (p *Proxy) logf(f string, args ...any) { p.cfg.Logf("podnester["+p.cfg.Owner+"]: "+f, args...) }

// EnsureNetwork creates the owner's network if it is missing. Do this before
// starting the owner, which must be run with --network on it.
func (p *Proxy) EnsureNetwork(ctx context.Context) error {
	_, err := p.up.inspectNetwork(ctx, p.Network())
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	return p.up.call(ctx, "POST", "/networks/create", nil, map[string]any{
		"Name": p.Network(), "Driver": "bridge", "Labels": map[string]string{p.cfg.Label: p.cfg.Owner},
	}, nil)
}

// ListenAndServe serves the socket in the control directory until ctx ends.
func (p *Proxy) ListenAndServe(ctx context.Context) error {
	sock := p.Socket()
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o666)
	return p.Serve(ctx, l)
}

// Serve serves the API on l until ctx ends, keeping forwarders in step with
// the siblings meanwhile and removing them all when the owner stops.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.stopForwarders() // leftovers of an earlier proxy on this directory
	go p.watchOwner(ctx)
	go func() {
		for {
			p.syncForwarders(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
		}
	}()
	srv := &http.Server{Handler: p}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	err := srv.Serve(l)
	p.stopForwarders()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// watchOwner removes the siblings when the owner stops: they are meant to
// live as long as its run, like processes in it. Volumes and networks stay,
// for the next run to pick up.
func (p *Proxy) watchOwner(ctx context.Context) {
	if insp, err := p.up.inspectContainer(ctx, p.cfg.Owner); err == nil && !insp.State.Running {
		p.ownerStopped(ctx)
	}
	for ctx.Err() == nil {
		filters, _ := json.Marshal(map[string][]string{"container": {p.cfg.Owner}, "event": {"die", "remove"}})
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://podman/events?"+url.Values{"filters": {string(filters)}}.Encode(), nil)
		resp, err := p.up.client.Do(req)
		if err == nil {
			dec := json.NewDecoder(resp.Body)
			for {
				var ev map[string]any
				if err := dec.Decode(&ev); err != nil {
					break
				}
				p.ownerStopped(ctx)
			}
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (p *Proxy) ownerStopped(ctx context.Context) {
	if insp, err := p.up.inspectContainer(ctx, p.cfg.Owner); err == nil && insp.State.Running {
		return // back already
	}
	if err := p.RemoveContainers(ctx); err != nil {
		p.logf("removing the siblings of a stopped owner: %v", err)
	}
}

// RemoveContainers removes every sibling (and their forwarders); volumes and
// networks stay.
func (p *Proxy) RemoveContainers(ctx context.Context) error {
	p.stopForwarders()
	list, err := p.up.listContainers(ctx, p.ownerLabel())
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range list {
		q := url.Values{"force": {"true"}}
		if err := p.up.call(ctx, "DELETE", "/containers/"+c.ID, q, nil, nil); err != nil && !isNotFound(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Purge removes everything of the owner's: containers, networks and (when
// asked) volumes. For when the owner is gone for good.
func (p *Proxy) Purge(ctx context.Context, volumes bool) error {
	errs := []error{p.RemoveContainers(ctx)}
	var nets []networkInspect
	if err := p.up.call(ctx, "GET", "/networks", url.Values{"filters": {labelFilter(p.ownerLabel())}}, nil, &nets); err != nil {
		errs = append(errs, err)
	}
	for _, n := range nets {
		if err := p.removeNetwork(ctx, n.Name); err != nil && !isNotFound(err) {
			errs = append(errs, err)
		}
	}
	if volumes {
		var vols struct{ Volumes []volumeInspect }
		if err := p.up.call(ctx, "GET", "/volumes", url.Values{"filters": {labelFilter(p.ownerLabel())}}, nil, &vols); err != nil {
			errs = append(errs, err)
		}
		for _, v := range vols.Volumes {
			if err := p.up.call(ctx, "DELETE", "/volumes/"+url.PathEscape(v.Name), url.Values{"force": {"true"}}, nil, nil); err != nil && !isNotFound(err) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// removeNetwork deletes a network of the owner's, disconnecting the owner
// from it first (podman keeps a network with containers on it).
func (p *Proxy) removeNetwork(ctx context.Context, name string) error {
	_ = p.up.call(ctx, "POST", "/networks/"+url.PathEscape(name)+"/disconnect", nil, map[string]any{"Container": p.cfg.Owner, "Force": true}, nil)
	return p.up.call(ctx, "DELETE", "/networks/"+url.PathEscape(name), nil, nil, nil)
}

// call is one request on its way through the proxy.
type call struct {
	p       *Proxy
	w       http.ResponseWriter
	r       *http.Request
	ctx     context.Context
	version string            // the API version prefix of the path, "/v1.41" or ""
	path    string            // the path without it, as the client sent it
	params  map[string]string // the route's placeholders
	query   url.Values

	stream bool            // the answer is a stream or a hijack: pass bytes through untouched
	mutate func(v any) any // applied to a JSON answer after the prefix is stripped
	after  func()          // run once podman has answered
}

type ctxKey struct{}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	version, path := splitVersion(r.URL.Path)
	c := &call{p: p, w: w, r: r, ctx: r.Context(), version: version, path: path, query: r.URL.Query()}
	route, params := matchRoute(r.Method, path)
	if route == nil {
		msg := fmt.Sprintf("podnester: %s %s is not supported through this socket", r.Method, path)
		if strings.HasPrefix(path, "/libpod/") {
			// The podman CLI in remote mode. Its ping is answered (see routes), so
			// that it gets this far and shows this rather than connection advice.
			msg = "podnester: podman's own API is not served through this socket, only docker's: use the docker command (podman's mirrors it) or docker compose"
		}
		writeError(w, http.StatusNotFound, msg)
		return
	}
	c.params = params
	if err := route.handle(c); err != nil {
		status, msg := http.StatusForbidden, err.Error()
		var he *httpError
		if errors.As(err, &he) {
			status = he.status
		} else if ae, ok := err.(*apiError); ok {
			status, msg = ae.Status, ae.Message
		}
		writeError(w, status, msg)
	}
}

// pass hands the (possibly rewritten) request to podman.
func (c *call) pass() error {
	c.r.URL.Path = c.version + c.path
	c.r.URL.RawPath = ""
	c.r.URL.RawQuery = c.query.Encode()
	c.r = c.r.WithContext(context.WithValue(c.ctx, ctxKey{}, c))
	c.p.rp.ServeHTTP(c.w, c.r)
	if c.after != nil {
		c.after()
	}
	return nil
}

// modifyResponse strips the prefix from JSON answers and applies the call's
// own changes; streams go through as they are.
func (p *Proxy) modifyResponse(resp *http.Response) error {
	c, _ := resp.Request.Context().Value(ctxKey{}).(*call)
	if c == nil || c.stream || resp.StatusCode == http.StatusSwitchingProtocols || resp.Body == nil {
		return nil
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	var v any
	if json.Unmarshal(data, &v) == nil {
		v = stripPrefix(v, p.cfg.Prefix)
		if c.mutate != nil && resp.StatusCode/100 == 2 {
			v = c.mutate(v)
		}
		data, _ = json.Marshal(v)
	}
	resp.Body = io.NopCloser(strings.NewReader(string(data)))
	resp.ContentLength = int64(len(data))
	resp.Header.Set("Content-Length", fmt.Sprint(len(data)))
	resp.Header.Del("Transfer-Encoding")
	return nil
}

// readJSON takes the request body in as a JSON object, for checking and
// rewriting; writeJSON puts the result back as the body.
func (c *call) readJSON() (map[string]json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(c.r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	obj := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return obj, nil
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, &httpError{http.StatusBadRequest, "podnester: the request body is not a JSON object: " + err.Error()}
	}
	return obj, nil
}

func (c *call) writeJSON(v any) {
	data, _ := json.Marshal(v)
	c.r.Body = io.NopCloser(strings.NewReader(string(data)))
	c.r.ContentLength = int64(len(data))
	c.r.Header.Set("Content-Type", "application/json")
}

// The owner's own resources, and whether a reference names one of them. A
// reference from a client is a name of theirs (which gets the prefix) or an
// id; either way the thing must carry the owner's label to be seen at all,
// and the path goes on with its id (or full name), never the client's word.

func (c *call) ownContainer(ref string) (*containerInspect, error) {
	insp, err := c.p.up.inspectContainer(c.ctx, prefixed(ref, c.p.cfg.Prefix))
	if isNotFound(err) || err == nil && insp.Config.Labels[c.p.cfg.Label] != c.p.cfg.Owner {
		return nil, &httpError{http.StatusNotFound, "No such container: " + ref}
	}
	return insp, err
}

func (c *call) ownNetwork(ref string) (*networkInspect, error) {
	if ref == "bridge" || ref == "default" {
		ref = c.p.Network()
	}
	n, err := c.p.up.inspectNetwork(c.ctx, prefixed(ref, c.p.cfg.Prefix))
	if isNotFound(err) || err == nil && n.Labels[c.p.cfg.Label] != c.p.cfg.Owner {
		return nil, &httpError{http.StatusNotFound, "No such network: " + ref}
	}
	return n, err
}

func (c *call) ownVolume(ref string) (*volumeInspect, error) {
	v, err := c.p.up.inspectVolume(c.ctx, prefixed(ref, c.p.cfg.Prefix))
	if isNotFound(err) || err == nil && v.Labels[c.p.cfg.Label] != c.p.cfg.Owner {
		return nil, &httpError{http.StatusNotFound, "No such volume: " + ref}
	}
	return v, err
}

// ensureVolume makes sure a named volume of the client's exists, as the
// owner's: podman would otherwise create it unlabeled on first use.
func (c *call) ensureVolume(name string) (string, error) {
	full := prefixed(name, c.p.cfg.Prefix)
	v, err := c.p.up.inspectVolume(c.ctx, full)
	if err == nil {
		if v.Labels[c.p.cfg.Label] != c.p.cfg.Owner {
			return "", denied("volume %s exists but is not yours", name)
		}
		return full, nil
	}
	if !isNotFound(err) {
		return "", err
	}
	err = c.p.up.call(c.ctx, "POST", "/volumes/create", nil, map[string]any{
		"Name": full, "Labels": map[string]string{c.p.cfg.Label: c.p.cfg.Owner},
	}, nil)
	return full, err
}

// ownerTable is the owner's filesystem view, for resolving bind sources.
func ownerTable(insp *containerInspect) *mountTable {
	t := &mountTable{Root: insp.GraphDriver.Data["MergedDir"], Mounts: map[string]string{}}
	for _, m := range insp.Mounts {
		if m.Type == "bind" && m.Source != "" && m.Destination != "" {
			t.Mounts[m.Destination] = m.Source
		}
	}
	if t.Root == "" {
		t.Root = "/nonexistent/podnester-root" // a storage driver without a merged view: only bind mounts can be shared
	}
	return t
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func denied(format string, args ...any) error {
	return &httpError{http.StatusForbidden, "podnester: " + fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) error {
	return &httpError{http.StatusBadRequest, "podnester: " + fmt.Sprintf(format, args...)}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

// splitVersion takes the API version prefix off a path: "/v1.41/containers/json".
func splitVersion(p string) (version, rest string) {
	if strings.HasPrefix(p, "/v") {
		if i := strings.Index(p[1:], "/"); i > 0 && isVersion(p[2:i+1]) {
			return p[:i+1], p[i+1:]
		}
		if isVersion(p[2:]) {
			return p, "/"
		}
	}
	return "", p
}

func isVersion(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c == '.') {
			return false
		}
	}
	return true
}

func unmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// Subcommand runs the helper role of a binary embedding the proxy — the
// port forwarder — when args (os.Args) ask for it.
// Call it first thing in main; handled says whether it did the job.
func Subcommand(args []string) (handled bool, err error) {
	if len(args) < 2 {
		return false, nil
	}
	switch args[1] {
	case "podnester-forward":
		rest := args[2:]
		control := ""
		if len(rest) >= 2 && rest[0] == "--control" {
			control, rest = rest[1], rest[2:]
		}
		if control == "" || len(rest) != 2 {
			return true, errors.New("usage: podnester-forward --control FILE LISTEN TARGET")
		}
		return true, Forward(control, rest[0], rest[1])
	}
	return false, nil
}

// OwnerExists says whether the owner container exists at all.
func (p *Proxy) OwnerExists(ctx context.Context) (bool, error) {
	_, err := p.up.inspectContainer(ctx, p.cfg.Owner)
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
