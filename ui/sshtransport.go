package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/vanviegen/tps/sshx"
)

// sshTransport reaches a daemon on another machine: it checks the host,
// installs our binary next to the daemon's data and starts the daemon when
// it is not running, all over one SSH connection.
type sshTransport struct {
	u       *UI
	c       *sshx.Client
	mu      sync.Mutex
	home    string // remote $HOME
	arch    string // remote uname -m
	systemd bool   // a user manager is available for systemd-run
	warning string
}

const remoteDir = ".local/share/tps" // under the remote home

func newSSHTransport(u *UI, hid, dest string) *sshTransport {
	return &sshTransport{u: u, c: sshx.New(dest, func(run string) []string { return u.askpass.Env(hid, run) })}
}

func (t *sshTransport) bin() string { return t.home + "/" + remoteDir + "/bin/tps" }

func (t *sshTransport) Warning() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.warning
}

func (t *sshTransport) DialDaemon() (net.Conn, error) {
	if err := t.preflight(); err != nil {
		return nil, err
	}
	// The relay that reaches the daemon socket is our own binary, so that goes first.
	if err := t.Push(); err != nil {
		return nil, err
	}
	sock := t.home + "/" + remoteDir + "/daemon.sock"
	if conn, err := t.c.DialUnix(t.bin(), sock); err == nil {
		return conn, nil
	}
	if err := t.startDaemon(); err != nil {
		return nil, err
	}
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		if conn, err := t.c.DialUnix(t.bin(), sock); err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("the daemon on %s did not come up; check its log there (journalctl --user, or %s/daemon.log)", t.c.Host(), remoteDir)
}

func (t *sshTransport) DialPort(port int) (net.Conn, error) {
	return t.c.DialTCP(port)
}

// preflight learns what the host offers and refuses hosts that lack podman or git.
func (t *sshTransport) preflight() error {
	out, err := t.c.Output(`echo "HOME=$HOME"; echo "ARCH=$(uname -m)"
for tool in podman git; do command -v $tool >/dev/null 2>&1 || echo "MISSING=$tool"; done
echo "LINGER=$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)"
echo "SYSTEMD=$(command -v systemd-run >/dev/null 2>&1 && systemctl --user is-system-running 2>/dev/null)"`)
	if err != nil {
		return err
	}
	var missing []string
	t.mu.Lock()
	defer t.mu.Unlock()
	t.warning = ""
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "HOME":
			t.home = v
		case "ARCH":
			t.arch = v
		case "MISSING":
			missing = append(missing, v)
		case "LINGER":
			if v != "yes" {
				t.warning = "Containers and the daemon may not outlive your login on " + t.c.Host() +
					": run `loginctl enable-linger` there (rootless podman needs it)"
			}
		case "SYSTEMD":
			t.systemd = v == "running" || v == "degraded"
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s lacks %s; TPS needs podman and git there", t.c.Host(), strings.Join(missing, " and "))
	}
	if t.home == "" {
		return errors.New("could not determine the remote home directory")
	}
	return nil
}

// Push installs the binary this UI runs (or --daemon-binary) on the host,
// unless the same build is already there.
func (t *sshTransport) Push() error {
	t.mu.Lock()
	arch, home := t.arch, t.home
	t.mu.Unlock()
	goarch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64", "riscv64": "riscv64"}[arch]
	source := t.u.daemonBinary
	if source == "" {
		if goarch != runtime.GOARCH || runtime.GOOS != "linux" {
			return fmt.Errorf("%s is linux/%s (%s); build a binary for it and pass --daemon-binary", t.c.Host(), goarch, arch)
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		source = exe
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	want := hex.EncodeToString(h.Sum(nil))
	bin := t.bin()
	if have, _ := t.c.Output("sha256sum " + bin + " 2>/dev/null | cut -c1-64"); have == want {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r, err := t.c.Run(fmt.Sprintf("mkdir -p %[1]s/bin && cat >%[2]s.new && chmod 755 %[2]s.new && mv -f %[2]s.new %[2]s", home+"/"+remoteDir, bin), f)
	if err == nil && r.Code != 0 {
		err = fmt.Errorf("installing the binary on %s failed: %s", t.c.Host(), strings.TrimSpace(r.Err))
	}
	return err
}

func (t *sshTransport) startDaemon() error {
	t.mu.Lock()
	home, systemd := t.home, t.systemd
	t.mu.Unlock()
	bin := t.bin()
	var cmd string
	if systemd {
		cmd = fmt.Sprintf("systemd-run --user --quiet --collect --unit tps-daemon-$(date +%%s) %s --daemon", bin)
	} else {
		cmd = fmt.Sprintf("(setsid nohup %s --daemon >>%s/daemon.log 2>&1 </dev/null &)", bin, home+"/"+remoteDir)
	}
	_, err := t.c.Output(cmd)
	return err
}
