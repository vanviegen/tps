package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vanviegen/podnester"
)

// Sub-containers. Every task container gets a docker socket of its own,
// served by podnester on the host: docker and docker compose in the
// container work against the host's podman, held to what the task may have.
// What a task starts through it are siblings of its container on a network
// of the task's own, labeled and prefixed with the container's name, removed
// along with it (named volumes stay until the task is deleted).

// nestMount is where the task's control directory (socket, forwarder files)
// is mounted in its container; nestDir is that directory in the task's dir.
const nestMount = "/run/podnester"

func nestDir(taskDir string) string { return filepath.Join(taskDir, "nest") }

var nests = struct {
	sync.Mutex
	m map[string]*nest
}{m: map[string]*nest{}}

type nest struct {
	proxy  *podnester.Proxy
	cancel context.CancelFunc
}

var podman struct {
	sync.Mutex
	sock string
	svc  *exec.Cmd // our own podman system service, when the host runs none
}

// podmanAPI is the podman API socket on this host: the one CONTAINER_HOST
// names, if it answers (podman's own way of being pointed at a service; in a
// TPS task container it is the task's docker socket, which is what lets TPS
// run inside TPS), else the user's podman.socket when systemd runs one, else
// a service of our own that dies with the daemon. The socket is picked anew
// whenever the last one stops answering, as a service of our own may die or
// have its socket removed while the daemon runs.
func podmanAPI() (string, error) {
	podman.Lock()
	defer podman.Unlock()
	if podman.sock != "" && podnester.SocketAnswers(podman.sock) {
		return podman.sock, nil
	}
	if podman.svc != nil {
		_ = podman.svc.Process.Kill()
		_ = podman.svc.Wait()
		podman.sock, podman.svc = "", nil
	}
	if sock, ok := strings.CutPrefix(os.Getenv("CONTAINER_HOST"), "unix://"); ok && podnester.SocketAnswers(sock) {
		podman.sock = sock
		return sock, nil
	}
	socks := []string{filepath.Join(home(), ".local", "share", "tps", "podman.sock")}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		socks = append([]string{filepath.Join(dir, "podman", "podman.sock")}, socks...)
	}
	var err error
	for _, sock := range socks {
		var svc *exec.Cmd
		if svc, err = podnester.EnsureService(sock); err == nil {
			podman.sock, podman.svc = sock, svc
			return sock, nil
		}
	}
	return "", err
}

// nestFor is the proxy serving a task container's socket, started if it
// was not running: before the container itself, so the socket is there when
// the container comes up, and again for a container adopted after a restart.
func nestFor(name, dir string) (*podnester.Proxy, error) {
	nests.Lock()
	defer nests.Unlock()
	if n := nests.m[name]; n != nil {
		return n.proxy, nil
	}
	sock, err := podmanAPI()
	if err != nil {
		return nil, err
	}
	p, err := podnester.New(podnester.Config{
		Upstream:     sock,
		Owner:        name,
		Control:      dir,
		ControlMount: nestMount,
		SecurityOpt:  []string{"label=disable"}, // as the task container runs
		Logf:         logf,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := p.ListenAndServe(ctx); err != nil {
			logf("%s: docker socket: %v", name, err)
		}
	}()
	nests.m[name] = &nest{p, cancel}
	return p, nil
}

// nestClear removes a task's sub-containers and their networks, its socket
// still served. They run in the task container's user namespace (podnester
// puts them there, so they can join each other's namespaces), which makes
// them dependents podman refuses to remove that container before: clearing
// them is where removing a task container begins, and a container being
// replaced needs them gone while its socket lives on.
func nestClear(name, dir string) {
	var p *podnester.Proxy
	nests.Lock()
	if n := nests.m[name]; n != nil {
		p = n.proxy
	}
	nests.Unlock()
	if p == nil {
		// A task this daemon never served (it restarted since) still has
		// everything it made labeled with its name, which is all this takes.
		if p = offNest(name, dir); p == nil {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.Clear(ctx); err != nil {
		logf("%s: removing sub-containers: %v", name, err)
	}
}

// offNest is a proxy for a task whose socket this daemon does not serve: it
// serves nothing itself, and is only good for working on what the task made.
// nil when there is no podman to talk to.
func offNest(name, dir string) *podnester.Proxy {
	sock, err := podmanAPI()
	if err != nil {
		return nil
	}
	// Forwarder set, as none is run: podnester would otherwise install its
	// binary in the control directory for it.
	p, err := podnester.New(podnester.Config{Upstream: sock, Owner: name, Control: dir, ControlMount: nestMount, Forwarder: []string{"-"}, Logf: logf})
	if err != nil {
		return nil
	}
	return p
}

// nestDown removes a task's sub-containers and stops serving its socket:
// its container is going down.
func nestDown(name, dir string) {
	nestClear(name, dir)
	nests.Lock()
	n := nests.m[name]
	delete(nests.m, name)
	nests.Unlock()
	if n != nil {
		n.cancel()
	}
}

// nestPurge removes everything a task made through its socket, volumes and
// networks included: the task itself is going away.
func nestPurge(name, dir string) {
	nestDown(name, dir)
	p := offNest(name, dir)
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := p.Purge(ctx, true); err != nil {
		logf("%s: purging sub-containers, networks and volumes: %v", name, err)
	}
}
