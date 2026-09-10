package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

var (
	podmanOnce sync.Once
	podmanSock string
	podmanSvc  *exec.Cmd // our own podman system service, when the host runs none
	podmanErr  error
)

// podmanAPI is the podman API socket on this host: the user's podman.socket
// when systemd runs one, else a service of our own that dies with the daemon.
func podmanAPI() (string, error) {
	podmanOnce.Do(func() {
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			sock := filepath.Join(dir, "podman", "podman.sock")
			if _, err := podnester.EnsureService(sock); err == nil {
				podmanSock = sock
				return
			}
		}
		podmanSock = filepath.Join(home(), ".local", "share", "tps", "podman.sock")
		podmanSvc, podmanErr = podnester.EnsureService(podmanSock)
	})
	return podmanSock, podmanErr
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
		UsernsMode:   "keep-id:uid=1000,gid=1000", // as the task container runs
		SecurityOpt:  []string{"label=disable"},   // likewise
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

// nestDown removes a task's sub-containers and stops serving its socket:
// its container is going down.
func nestDown(name string) {
	nests.Lock()
	n := nests.m[name]
	delete(nests.m, name)
	nests.Unlock()
	if n == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := n.proxy.RemoveContainers(ctx); err != nil {
		logf("%s: removing sub-containers: %v", name, err)
	}
	n.cancel()
}

// nestPurge removes everything a task made through its socket, volumes and
// networks included: the task itself is going away.
func nestPurge(name, dir string) {
	nestDown(name)
	sock, err := podmanAPI()
	if err != nil {
		return
	}
	p, err := podnester.New(podnester.Config{Upstream: sock, Owner: name, Control: dir, ControlMount: nestMount, Forwarder: []string{"-"}, Resolver: []string{"-"}, Logf: logf})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := p.Purge(ctx, true); err != nil {
		logf("%s: purging sub-containers, networks and volumes: %v", name, err)
	}
}
