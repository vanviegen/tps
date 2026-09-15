package podnester

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakePodman is enough of the API for the proxy's checks to run against:
// an owner to inspect, resources to own or not, and a record of what the
// proxy sent on.
type fakePodman struct {
	t        *testing.T
	root     string
	work     string
	requests []*http.Request
	bodies   []map[string]any
	readlink string        // what the last readlink in the owner answers
	userns   string        // the owner's UsernsMode as inspected; "private" if empty
	release  chan struct{} // a wait answers its headers at once and its body when this closes
}

func (f *fakePodman) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	f.requests = append(f.requests, r)
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	f.bodies = append(f.bodies, body)
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	label := map[string]any{"podnester.owner": "top"}
	_, p := splitVersion(r.URL.Path)
	switch {
	case p == "/_ping" || p == "/libpod/_ping":
		w.Write([]byte("OK"))
	case p == "/networks/create":
		w.WriteHeader(201)
		reply(map[string]any{"Id": "n3", "Warning": ""})
	case p == "/containers/1111111111111111/wait":
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if f.release != nil {
			<-f.release
		}
		reply(map[string]any{"Error": nil, "StatusCode": 0})
	case p == "/containers/top/exec": // readlink -f in the owner: the fake's links
		var body struct{ Cmd []string }
		_ = json.Unmarshal(data, &body)
		f.readlink = map[string]string{"/work/data": "/work/data", "/work/escape": "/home/nobody", "/": "/"}[body.Cmd[len(body.Cmd)-1]]
		w.WriteHeader(201)
		reply(map[string]any{"Id": "e1"})
	case p == "/exec/e1/start":
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		if f.readlink == "" {
			return // nothing on stdout: no such path
		}
		out := []byte(f.readlink + "\n")
		head := []byte{1, 0, 0, 0, 0, 0, 0, byte(len(out))}
		w.Write(append(head, out...))
	case p == "/exec/e1/json":
		code := 0
		if f.readlink == "" {
			code = 1
		}
		reply(map[string]any{"ContainerID": "aaaa", "ExitCode": code})
	case p == "/containers/top/json":
		userns := f.userns
		if userns == "" {
			userns = "private"
		}
		reply(map[string]any{"Id": "aaaa", "Name": "/top", "State": map[string]any{"Running": true},
			"HostConfig":  map[string]any{"UsernsMode": userns},
			"Config":      map[string]any{"Labels": map[string]any{}},
			"GraphDriver": map[string]any{"Data": map[string]any{"MergedDir": f.root}},
			"Mounts":      []any{map[string]any{"Type": "bind", "Source": f.work, "Destination": "/work"}}})
	case p == "/containers/top-db/json" || p == "/containers/1111111111111111/json":
		reply(map[string]any{"Id": "1111111111111111", "Name": "/top-db", "State": map[string]any{"Running": true},
			"Config": map[string]any{"Labels": label}})
	case p == "/containers/top-other/json" || p == "/containers/other/json":
		reply(map[string]any{"Id": "2222222222222222", "Name": "/other", "Config": map[string]any{"Labels": map[string]any{"podnester.owner": "someone"}}})
	case p == "/networks/top-bridge":
		reply(map[string]any{"Name": "top-bridge", "Id": "n1", "Labels": label})
	case p == "/networks/top-mynet":
		reply(map[string]any{"Name": "top-mynet", "Id": "n2", "Labels": label})
	case p == "/volumes/top-data":
		reply(map[string]any{"Name": "top-data", "Labels": label})
	case p == "/volumes/create":
		w.WriteHeader(201)
		reply(map[string]any{"Name": body["Name"]})
	case p == "/containers/create":
		w.WriteHeader(201)
		reply(map[string]any{"Id": "3333333333333333", "Warnings": []any{}})
	case p == "/containers/json":
		reply([]any{map[string]any{"Id": "1111111111111111", "Names": []any{"/top-db"}, "Labels": map[string]any{"podnester.ports": `{"5432/tcp":[{"HostIp":"","HostPort":"5432"}]}`}}})
	case strings.HasPrefix(p, "/networks/") && strings.HasSuffix(p, "/connect"):
		w.WriteHeader(200)
	default:
		w.WriteHeader(404)
		reply(map[string]any{"message": "no such thing: " + p})
	}
}

func newTestProxy(t *testing.T) (*Proxy, *fakePodman) {
	f := &fakePodman{t: t, root: t.TempDir(), work: t.TempDir()}
	return proxyOn(t, f), f
}

// proxyOn is a proxy talking to this stand-in for podman.
func proxyOn(t *testing.T, upstream http.Handler) *Proxy {
	sock := filepath.Join(t.TempDir(), "podman.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(upstream)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	p, err := New(Config{Upstream: sock, Owner: "top", Control: t.TempDir(), ControlMount: "/run/podnester",
		SecurityOpt: []string{"label=disable"},
		Forwarder:   []string{"/nonexistent"}, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func do(p *Proxy, method, path string, body any) *httptest.ResponseRecorder {
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = strings.NewReader(string(data))
	}
	req := httptest.NewRequest(method, path, r)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestCreatePolicy(t *testing.T) {
	p, f := newTestProxy(t)
	os.MkdirAll(filepath.Join(f.work, "data"), 0o755)
	os.Symlink("/home/nobody", filepath.Join(f.work, "escape"))

	ok := map[string]any{
		"Image": "postgres", "Env": []string{"A=b"}, "Labels": map[string]string{"x": "y"},
		"HostConfig": map[string]any{
			"Binds":           []string{"/work/data:/data:ro,Z", "pgdata:/var/lib/postgresql"},
			"Sysctls":         map[string]string{"net.unix.max_dgram_qlen": "512", "kernel.shmmax": "1"},
			"ContainerIDFile": "/tmp/cid",
			"Mounts":          []any{map[string]any{"Type": "bind", "Source": "/work/data", "Target": "/d2"}},
			"PortBindings":    map[string]any{"5432/tcp": []any{map[string]any{"HostIp": "", "HostPort": "5432"}}},
			"Privileged":      false, "CapAdd": nil, "Devices": []any{}, "NetworkMode": "default",
			"UsernsMode": "host", "SecurityOpt": []string{"no-new-privileges"},
			"PidMode": "container:db", "VolumesFrom": []string{"db:ro"},
		},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{"mynet": map[string]any{"Aliases": []string{"pg"}}}},
	}
	rec := do(p, "POST", "/v1.41/containers/create?name=pg", ok)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if w, _ := resp["Warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "UsernsMode") {
		t.Errorf("warnings: %v", resp["Warnings"])
	}
	last := f.requests[len(f.requests)-1]
	if last.URL.Path != "/v1.41/containers/create" || last.URL.Query().Get("name") != "top-pg" {
		t.Errorf("sent on as %s", last.URL)
	}
	sent := f.bodies[len(f.bodies)-1]
	hc := sent["HostConfig"].(map[string]any)
	binds := hc["Binds"].([]any)
	if binds[0] != filepath.Join(f.work, "data")+":/data:ro" || binds[1] != "top-pgdata:/var/lib/postgresql" {
		t.Errorf("binds: %v", binds)
	}
	if m := hc["Mounts"].([]any)[0].(map[string]any); m["Source"] != filepath.Join(f.work, "data") {
		t.Errorf("mounts: %v", m)
	}
	if hc["NetworkMode"] != "top-bridge" || hc["UsernsMode"] != "container:top" || hc["PidMode"] != "container:1111111111111111" {
		t.Errorf("modes: %v", hc)
	}
	if so := hc["SecurityOpt"].([]any); len(so) != 2 || so[1] != "label=disable" {
		t.Errorf("security opt: %v", so)
	}
	if vf := hc["VolumesFrom"].([]any); vf[0] != "1111111111111111:ro" {
		t.Errorf("volumes from: %v", vf)
	}
	if pb := hc["PortBindings"].(map[string]any); len(pb) != 0 {
		t.Errorf("port bindings passed to podman: %v", pb)
	}
	if s := hc["Sysctls"].(map[string]any); s["net.unix.max_dgram_qlen"] != "512" || s["kernel.shmmax"] != "1" {
		t.Errorf("sysctls: %v", s)
	}
	if _, ok := hc["ContainerIDFile"]; ok {
		t.Errorf("ContainerIDFile passed to podman")
	}
	labels := sent["Labels"].(map[string]any)
	if labels["podnester.owner"] != "top" || labels["x"] != "y" || !strings.Contains(labels["podnester.ports"].(string), "5432") {
		t.Errorf("labels: %v", labels)
	}
	if ep := sent["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any); ep["top-mynet"] == nil {
		t.Errorf("endpoints: %v", ep)
	}
	created := false
	for i, r := range f.requests {
		if r.URL.Path == "/volumes/create" && f.bodies[i]["Name"] == "top-pgdata" {
			created = true
		}
	}
	if !created {
		t.Error("the named volume was not created as the owner's")
	}

	denied := []map[string]any{
		{"Image": "x", "HostConfig": map[string]any{"Privileged": true}},
		{"Image": "x", "HostConfig": map[string]any{"CapAdd": []string{"SYS_ADMIN"}}},
		{"Image": "x", "HostConfig": map[string]any{"Devices": []any{map[string]any{"PathOnHost": "/dev/fuse"}}}},
		{"Image": "x", "HostConfig": map[string]any{"NetworkMode": "host"}},
		{"Image": "x", "HostConfig": map[string]any{"PidMode": "host"}},
		{"Image": "x", "HostConfig": map[string]any{"Binds": []string{"/work/data:/d:upperdir=/tmp"}}},
		{"Image": "x", "HostConfig": map[string]any{"Binds": []string{"/work/missing:/d"}}}, // a mount source must exist in the owner
		{"Image": "x", "HostConfig": map[string]any{"Mounts": []any{map[string]any{"Type": "bind", "Source": "/work", "Target": "/d", "BindOptions": map[string]any{"Propagation": "rshared"}}}}},
		{"Image": "x", "HostConfig": map[string]any{"SecurityOpt": []string{"seccomp=unconfined"}}},
		{"Image": "x", "HostConfig": map[string]any{"Sysctls": map[string]string{"vm.overcommit_memory": "1"}}},
		{"Image": "x", "HostConfig": map[string]any{"Sysctls": map[string]string{"kernel.domainname": "x"}}},
		{"Image": "x", "HostConfig": map[string]any{"Sysctls": map[string]string{"net.ipv4.ip_forward": "1"}, "NetworkMode": "container:db"}},
		{"Image": "x", "HostConfig": map[string]any{"Sysctls": map[string]string{"kernel.shmmax": "1"}, "IpcMode": "container:db"}},
		{"Image": "x", "HostConfig": map[string]any{"PidMode": "container:other"}},
		{"Image": "x", "HostConfig": map[string]any{"NewShinyOption": true}},
		{"Image": "x", "SomethingNew": 1},
		{"Image": "x", "HostConfig": map[string]any{"LogConfig": map[string]any{"Type": "k8s-file", "Config": map[string]string{"path": "/etc/shadow"}}}},
		{"Image": "x", "NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{"podman": map[string]any{}}}},
	}
	for _, body := range denied {
		rec := do(p, "POST", "/containers/create", body)
		if rec.Code/100 == 2 {
			t.Errorf("accepted: %v", body)
		}
	}
	// A port of a protocol the forwarders do not serve: kept out of the label, with a warning, rather than refused.
	rec = do(p, "POST", "/containers/create", map[string]any{"Image": "x", "HostConfig": map[string]any{
		"PortBindings": map[string]any{"53/udp": []any{map[string]any{"HostPort": "53"}}, "80/tcp": []any{map[string]any{"HostPort": "8080"}}}}})
	if rec.Code != 201 {
		t.Fatalf("udp binding: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if w, _ := resp["Warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "53/udp") {
		t.Errorf("warnings about the udp binding: %v", resp["Warnings"])
	}
	if ports := f.bodies[len(f.bodies)-1]["Labels"].(map[string]any)["podnester.ports"]; ports != `{"80/tcp":[{"HostIp":"","HostPort":"8080"}]}` {
		t.Errorf("ports label: %v", ports)
	}
	// The host's root, through the owner's own root filesystem: allowed, as the owner sees it — mapped under the merged dir.
	rec = do(p, "POST", "/containers/create", map[string]any{"Image": "x", "HostConfig": map[string]any{"Binds": []string{"/:/hostroot"}}})
	if rec.Code != 201 {
		t.Fatalf("root bind: %d %s", rec.Code, rec.Body)
	}
	if b := f.bodies[len(f.bodies)-1]["HostConfig"].(map[string]any)["Binds"].([]any)[0]; b != f.root+":/hostroot" {
		t.Errorf("root bind sent as %v", b)
	}
	// A link in the owner's files pointing at an absolute path means that path in the owner, not on the host.
	rec = do(p, "POST", "/containers/create", map[string]any{"Image": "x", "HostConfig": map[string]any{"Binds": []string{"/work/escape:/d"}}})
	if rec.Code != 201 {
		t.Fatalf("link bind: %d %s", rec.Code, rec.Body)
	}
	if b := f.bodies[len(f.bodies)-1]["HostConfig"].(map[string]any)["Binds"].([]any)[0]; b != filepath.Join(f.root, "home", "nobody")+":/d" {
		t.Errorf("link bind sent as %v", b)
	}
}

func TestOwnership(t *testing.T) {
	p, f := newTestProxy(t)
	if rec := do(p, "GET", "/containers/db/json", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Name":"/db"`) {
		t.Errorf("own container: %d %s", rec.Code, rec.Body)
	}
	if last := f.requests[len(f.requests)-1]; last.URL.Path != "/containers/1111111111111111/json" {
		t.Errorf("sent on as %s", last.URL.Path)
	}
	if rec := do(p, "GET", "/containers/other/json", nil); rec.Code != 404 {
		t.Errorf("someone else's container: %d", rec.Code)
	}
	if rec := do(p, "GET", "/containers/top/json", nil); rec.Code != 404 {
		t.Errorf("the owner itself: %d", rec.Code)
	}
	if rec := do(p, "DELETE", "/images/alpine", nil); rec.Code != 404 {
		t.Errorf("image removal: %d", rec.Code)
	}
	if rec := do(p, "POST", "/auth", nil); rec.Code != 404 {
		t.Errorf("registry login: %d", rec.Code)
	}
	if rec := do(p, "GET", "/v4.0.0/libpod/containers/json", nil); rec.Code != 404 {
		t.Errorf("libpod API: %d", rec.Code)
	}
	if rec := do(p, "GET", "/v4.0.0/libpod/_ping", nil); rec.Code == 404 {
		t.Errorf("libpod ping: %d", rec.Code)
	}
	if rec := do(p, "GET", "/v4.0.0/libpod/info", nil); !strings.Contains(rec.Body.String(), "use the docker command") {
		t.Errorf("libpod refusal: %s", rec.Body)
	}
	rec := do(p, "GET", "/containers/json?filters=%7B%22name%22%3A%5B%22db%22%5D%7D", nil)
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	last := f.requests[len(f.requests)-1]
	filters, _ := parseFilters(last.URL.Query().Get("filters"))
	if filters["label"][0] != "podnester.owner=top" || filters["name"][0] != "top-db" {
		t.Errorf("list filters: %v", filters)
	}
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if list[0]["Names"].([]any)[0] != "/db" {
		t.Errorf("list names: %v", list[0]["Names"])
	}
	if ports := list[0]["Ports"].([]any); len(ports) != 1 || ports[0].(map[string]any)["PublicPort"] != 5432.0 {
		t.Errorf("list ports: %v", list[0]["Ports"])
	}
	rec = do(p, "POST", "/networks/create", map[string]any{"Name": "app_default", "Driver": "bridge", "IPAM": map[string]any{"Driver": "default", "Config": []any{}}})
	if rec.Code == 404 || rec.Code == 403 {
		t.Errorf("compose network: %d %s", rec.Code, rec.Body)
	}
	sent := f.bodies[len(f.bodies)-2]
	if sent["Name"] != "top-app_default" || sent["Labels"].(map[string]any)["podnester.owner"] != "top" {
		t.Errorf("network create sent as %v", sent)
	}
	if last := f.requests[len(f.requests)-1]; last.URL.Path != "/networks/top-app_default/connect" {
		t.Errorf("the owner was not joined to the new network: %s", last.URL.Path)
	}
}

// A wait is a long poll: podman answers 200 at once and the body when the
// container exits, and the docker CLI's run blocks on the headers before it
// even starts the container. So they must go through as they come.
func TestWaitStreams(t *testing.T) {
	p, f := newTestProxy(t)
	f.release = make(chan struct{})
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+"/v1.41/containers/db/wait?condition=next-exit", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("wait: %d", resp.StatusCode)
	}
	close(f.release) // the headers arrived while podman still holds the body: now let the container "exit"
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"StatusCode":0`) {
		t.Errorf("wait body: %s", body)
	}
}

func TestBuildQuery(t *testing.T) {
	p, f := newTestProxy(t)
	if rec := do(p, "POST", "/v1.41/build?t=x&cgroupparent=&memory=0&networkmode=default", nil); rec.Code == 403 {
		t.Errorf("the docker CLI's own build query was refused: %s", rec.Body)
	}
	if q := f.requests[len(f.requests)-1].URL.Query(); q.Has("cgroupparent") || q.Get("t") != "x" {
		t.Errorf("sent on with %v", q)
	}
	if rec := do(p, "POST", "/build?t=x&cgroupparent=/elsewhere", nil); rec.Code != 403 {
		t.Errorf("a cgroup parent got through: %d", rec.Code)
	}
	if rec := do(p, "POST", "/build?t=x&securityopt=seccomp=unconfined", nil); rec.Code != 403 {
		t.Errorf("an unknown build parameter got through: %d", rec.Code)
	}
}

func TestOverlayBind(t *testing.T) {
	p, f := newTestProxy(t)
	os.MkdirAll(filepath.Join(f.work, "data"), 0o755)
	rec := do(p, "POST", "/containers/create", map[string]any{"Image": "x", "HostConfig": map[string]any{"Binds": []string{"/work/data:/app:O"}}})
	if rec.Code != 201 {
		t.Fatalf("overlay bind: %d %s", rec.Code, rec.Body)
	}
	if b := f.bodies[len(f.bodies)-1]["HostConfig"].(map[string]any)["Binds"].([]any)[0]; b != filepath.Join(f.work, "data")+":/app:O" {
		t.Errorf("overlay bind sent as %v", b)
	}
	// Its upper and work directories are podman's to place; naming host paths for them is not on.
	rec = do(p, "POST", "/containers/create", map[string]any{"Image": "x", "HostConfig": map[string]any{"Binds": []string{"/work/data:/app:O,upperdir=/tmp/up,workdir=/tmp/wk"}}})
	if rec.Code != 403 {
		t.Errorf("overlay with host dirs: %d %s", rec.Code, rec.Body)
	}
}

// Siblings join the owner's user namespace; an owner without one of its own
// runs in podman's, as do siblings by default.
func TestUserns(t *testing.T) {
	p, f := newTestProxy(t)
	f.userns = "host"
	if rec := do(p, "POST", "/containers/create", map[string]any{"Image": "x"}); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if hc := f.bodies[len(f.bodies)-1]["HostConfig"].(map[string]any); hc["UsernsMode"] != nil {
		t.Errorf("userns set for an owner without one: %v", hc["UsernsMode"])
	}
	f.userns = "private"
	rec := do(p, "POST", "/containers/create", map[string]any{"Image": "x", "HostConfig": map[string]any{"UsernsMode": "keep-id"}})
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), "UsernsMode") {
		t.Fatalf("create with a userns of its own: %d %s", rec.Code, rec.Body)
	}
	if hc := f.bodies[len(f.bodies)-1]["HostConfig"].(map[string]any); hc["UsernsMode"] != "container:top" {
		t.Errorf("userns: %v", hc["UsernsMode"])
	}
}

// Siblings can be in each other's namespaces, and podman refuses to remove a
// container another one depends on: removing them all takes another pass at
// whatever refused, and stops when a pass removes nothing.
func TestRemoveContainersRetries(t *testing.T) {
	removable := func(stuck bool) (http.Handler, map[string]bool) {
		var mu sync.Mutex
		gone := map[string]bool{}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			_, path := splitVersion(r.URL.Path)
			id := strings.TrimPrefix(path, "/containers/")
			w.Header().Set("Content-Type", "application/json")
			switch {
			case path == "/containers/json":
				list := []any{}
				for _, c := range []string{"a", "b"} {
					if !gone[c] {
						list = append(list, map[string]any{"Id": c})
					}
				}
				json.NewEncoder(w).Encode(list)
			case r.Method == "DELETE" && (stuck || (id == "a" && !gone["b"])):
				w.WriteHeader(500)
				json.NewEncoder(w).Encode(map[string]any{"message": "container " + id + " has dependent containers which must be removed before it"})
			case r.Method == "DELETE":
				gone[id] = true
				w.WriteHeader(204)
			default:
				w.WriteHeader(404)
			}
		}), gone
	}
	// a is only removable once b is gone, and the list has it first.
	h, gone := removable(false)
	if err := proxyOn(t, h).RemoveContainers(context.Background()); err != nil {
		t.Errorf("removing the siblings: %v", err)
	}
	if !gone["a"] || !gone["b"] {
		t.Errorf("left behind: %v", gone)
	}
	// Nothing can go: the error is reported rather than retried forever.
	h, _ = removable(true)
	if err := proxyOn(t, h).RemoveContainers(context.Background()); err == nil {
		t.Error("a removal that gets nowhere is reported as done")
	}
}
