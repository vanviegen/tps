package podnester

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAgainstPodman runs the proxy against a real podman API service:
// PODNESTER_PODMAN_SOCKET names its socket (a `podman system service`, or
// the user's podman.socket). Skipped otherwise.
func TestAgainstPodman(t *testing.T) {
	sock := os.Getenv("PODNESTER_PODMAN_SOCKET")
	if sock == "" {
		t.Skip("PODNESTER_PODMAN_SOCKET not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	up := newUpstream(sock)
	if err := up.call(ctx, "GET", "/_ping", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	owner := "pntest-" + randomName()
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello"), []byte("hi"), 0o644)
	p, err := New(Config{Upstream: sock, Owner: owner, Control: t.TempDir(), ControlMount: "/run/podnester", SecurityOpt: []string{"label=disable"}, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = up.call(ctx, "DELETE", "/containers/"+owner, url.Values{"force": {"true"}}, nil, nil)
		if err := p.Purge(ctx, true); err != nil {
			t.Errorf("purge: %v", err)
		}
	}()
	// The image, and the owner: on the proxy's network, with a bind mount.
	if err := up.call(ctx, "POST", "/images/create", url.Values{"fromImage": {"docker.io/library/alpine:latest"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := up.call(ctx, "POST", "/containers/create", url.Values{"name": {owner}}, map[string]any{
		"Image": "docker.io/library/alpine:latest", "Cmd": []string{"sleep", "600"},
		// A user namespace of its own, as a rootless owner typically has: siblings join it.
		"HostConfig": map[string]any{"NetworkMode": p.Network(), "Binds": []string{work + ":/work"}, "UsernsMode": "keep-id"},
	}, &created); err != nil {
		t.Fatal(err)
	}
	running := up.call(ctx, "POST", "/containers/"+owner+"/start", nil, nil, nil) == nil

	srv := httptest.NewServer(p)
	defer srv.Close()
	do := func(method, path string, body any) (int, map[string]any, []map[string]any) {
		var r *strings.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			r = strings.NewReader(string(data))
		} else {
			r = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var obj map[string]any
		var list []map[string]any
		dec := json.NewDecoder(resp.Body)
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			var raw json.RawMessage
			_ = dec.Decode(&raw)
			_ = json.Unmarshal(raw, &obj)
			_ = json.Unmarshal(raw, &list)
		}
		return resp.StatusCode, obj, list
	}

	// A sibling with a bind from the owner's mount (resolved by the owner, so
	// only with it running), a named volume and a published port.
	binds := []string{"data:/d"}
	if running {
		binds = append([]string{"/work:/w"}, binds...)
	}
	code, obj, _ := do("POST", "/v1.41/containers/create?name=db", map[string]any{
		"Image": "docker.io/library/alpine:latest", "Cmd": []string{"nc", "-l", "-p", "5432", "-e", "cat"},
		"HostConfig": map[string]any{
			"Binds":        binds,
			"PortBindings": map[string]any{"5432/tcp": []any{map[string]any{"HostPort": "15432"}}},
		},
	})
	if code != 201 {
		t.Fatalf("create: %d %v", code, obj)
	}
	code, obj, _ = do("GET", "/containers/db/json", nil)
	if code != 200 || obj["Name"] != "/db" {
		t.Fatalf("inspect: %d %v", code, obj["Name"])
	}
	hc := obj["HostConfig"].(map[string]any)
	if got := hc["Binds"].([]any); running && !strings.HasPrefix(got[0].(string), work+":/w") || !strings.HasPrefix(got[len(got)-1].(string), "data:/d") {
		t.Errorf("binds: %v", got)
	}
	if hc["NetworkMode"] != "bridge" {
		t.Errorf("network mode: %v", hc["NetworkMode"])
	}
	if pb := hc["PortBindings"].(map[string]any)["5432/tcp"]; pb == nil {
		t.Errorf("port bindings not reported: %v", hc["PortBindings"])
	}
	// The host sees the prefix and the label; the client does not.
	insp, err := up.inspectContainer(ctx, owner+"-db")
	if err != nil || insp.Config.Labels[p.cfg.Label] != owner {
		t.Fatalf("host side: %v %v", err, insp)
	}
	code, _, list := do("GET", "/containers/json?all=true", nil)
	if code != 200 || len(list) != 1 || list[0]["Names"].([]any)[0] != "/db" {
		t.Errorf("list: %d %v", code, list)
	}
	// Someone else's container is invisible, the owner included.
	if code, _, _ := do("GET", "/containers/"+owner+"/json", nil); code != 404 {
		t.Errorf("owner visible: %d", code)
	}
	// A compose-style network: the owner joins it.
	code, obj, _ = do("POST", "/networks/create", map[string]any{"Name": "app_default", "Driver": "bridge"})
	if code != 201 {
		t.Fatalf("network create: %d %v", code, obj)
	}
	n, err := up.inspectNetwork(ctx, owner+"-app_default")
	if err != nil || n.Labels[p.cfg.Label] != owner {
		t.Fatalf("network on the host: %v", err)
	}
	joined := false
	for _, c := range n.Containers {
		if m, ok := c.(map[string]any); ok && m["Name"] == owner {
			joined = true
		}
	}
	if !joined {
		t.Errorf("the owner did not join the network: %v", n.Containers)
	}
	if code, _, _ := do("DELETE", "/networks/app_default", nil); code/100 != 2 {
		t.Errorf("network delete: %d", code)
	}
	// Refusals.
	for _, body := range []map[string]any{
		{"Image": "alpine", "HostConfig": map[string]any{"Privileged": true}},
		{"Image": "alpine", "HostConfig": map[string]any{"Binds": []string{"/work/../../../../etc:/e", "/x:/y:z"}}},
		{"Image": "alpine", "HostConfig": map[string]any{"NetworkMode": "host"}},
	} {
		if code, obj, _ := do("POST", "/containers/create", body); code/100 == 2 {
			t.Errorf("accepted %v: %v", body, obj)
		}
	}
	if code, _, _ := do("DELETE", "/images/alpine", nil); code != 404 {
		t.Errorf("image delete: %d", code)
	}
	if !running {
		t.Log("the owner could not be started here (a nested podman?): skipping the forwarder check")
		return
	}
	// With the owner running, a started sibling gets its port forwarded in the owner.
	if code, obj, _ := do("POST", "/containers/db/start", nil); code/100 != 2 {
		t.Fatalf("start: %d %v", code, obj)
	}
	// A sibling in db's network namespace: possible only within one user namespace, which they share with the owner.
	code, obj, _ = do("POST", "/containers/create?name=probe", map[string]any{
		"Image": "docker.io/library/alpine:latest", "Cmd": []string{"cat", "/sys/class/net/eth0/address"},
		"HostConfig": map[string]any{"NetworkMode": "container:db"},
	})
	if code != 201 {
		t.Fatalf("create in db's network namespace: %d %v", code, obj)
	}
	if code, obj, _ := do("POST", "/containers/probe/start", nil); code/100 != 2 {
		t.Fatalf("start in db's network namespace: %d %v", code, obj)
	}
	if code, obj, _ := do("POST", "/containers/probe/wait", nil); code != 200 || obj["StatusCode"] != 0.0 {
		t.Errorf("in db's network namespace: %d %v", code, obj)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		p.fwdMu.Lock()
		n := len(p.fwds)
		p.fwdMu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	code, obj, _ = do("GET", "/containers/db/json", nil)
	ports := obj["NetworkSettings"].(map[string]any)["Ports"].(map[string]any)
	if ports["5432/tcp"] == nil {
		t.Errorf("published port not reported: %v", ports)
	}
	// The owner reaches it on its own localhost.
	var ex struct {
		ID string `json:"Id"`
	}
	if err := up.call(ctx, "POST", "/containers/"+owner+"/exec", nil, map[string]any{"Cmd": []string{"sh", "-c", "echo ping | nc 127.0.0.1 15432"}, "AttachStdout": true}, &ex); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://podman/exec/"+ex.ID+"/start", strings.NewReader(`{"Detach":false}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := up.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 64)
	n2, _ := resp.Body.Read(out)
	resp.Body.Close()
	if !strings.Contains(string(out[:n2]), "ping") {
		t.Errorf("through the forwarder: %q", out[:n2])
	}
	// Removing the siblings gets every one of them, whichever order podman
	// wants: probe is in db's network namespace, and a container another one
	// depends on is not removed before it.
	if err := p.RemoveContainers(ctx); err != nil {
		t.Errorf("removing the siblings: %v", err)
	}
	if list, err := up.listContainers(ctx, p.ownerLabel()); err != nil || len(list) != 0 {
		t.Errorf("siblings left behind: %v %v", err, list)
	}
}
