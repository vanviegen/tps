package daemon

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// A code-server on the project's own checkout. It runs here on the host rather
// than in a container: this is the user's real working copy, and their git
// config, keys and tooling belong around it. Tasks get the containerised one
// instead (see container.go), over their own clone.
type codeServer struct {
	cmd  *exec.Cmd
	port int
	done chan struct{}
}

func (c *codeServer) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// kill stops the process and waits for the watcher goroutine to notice.
func (c *codeServer) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	<-c.done
}

// One at a time across projects: starting a code-server is rare, and the
// toolbox download it may wait on is serialized anyway.
var codeMu sync.Mutex

// bgOpenCode brings the code-server up without making the dashboard wait for
// it: on a fresh host that includes downloading the toolbox. The dashboard
// watches `codePort` (and `codeError`) for the outcome.
func (p *Project) bgOpenCode() {
	p.pub("codeError", nil)
	go func() {
		if err := p.openCode(); err != nil {
			logf("code-server for %s: %v", p.pid, err)
			p.pub("codeError", err.Error())
		}
	}()
}

// openCode makes sure a code-server serves this project's checkout, and
// publishes the port it listens on.
func (p *Project) openCode() error {
	codeMu.Lock()
	defer codeMu.Unlock()
	if p.code != nil && p.code.alive() {
		return nil
	}
	toolbox, err := ensureToolbox()
	if err != nil {
		return err
	}
	// The same settings, keybindings and extensions the task containers get,
	// so this VS Code doesn't open in a different theme than the ones next to it.
	vscode, err := sharedVscodeDir()
	if err != nil {
		return err
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(toolbox, "bin", "code-server"),
		"--bind-addr", fmt.Sprintf("127.0.0.1:%d", port),
		"--auth", "none", "--disable-workspace-trust",
		"--user-data-dir", vscode, "--extensions-dir", filepath.Join(vscode, "extensions"),
		p.dir())
	cmd.Dir = p.dir()
	// The toolbox first on PATH, so terminals in this VS Code can run claude too.
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(toolbox, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DISABLE_AUTOUPDATER=1") // the toolbox is read-only, and versioned by TPS
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting code-server: %w", err)
	}
	c := &codeServer{cmd: cmd, port: port, done: make(chan struct{})}
	// `done` closes before the lock is taken, so kill() below can never wait on
	// a goroutine that is waiting on us.
	go func() {
		_ = cmd.Wait()
		close(c.done)
		codeMu.Lock()
		defer codeMu.Unlock()
		if p.code == c {
			p.code = nil
			p.pub("codePort", nil)
		}
	}()
	p.code = c
	if err := waitCode(port); err != nil {
		p.code = nil
		c.kill()
		return err
	}
	p.pub("codePort", port)
	return nil
}

// closeCode stops the code-server, if one is running.
func (p *Project) closeCode() {
	codeMu.Lock()
	c := p.code
	p.code = nil
	codeMu.Unlock()
	if c != nil {
		c.kill()
		p.pub("codePort", nil)
	}
}

// freePort picks a loopback port for the code-server to bind. Racy in
// principle; in practice nothing else here is handing out ports.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitCode(port int) error {
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < 60; i++ {
		if resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port)); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("code-server did not come up on port %d", port)
}
