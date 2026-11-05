package ui

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/vanviegen/tps/daemon"
)

// transport reaches one daemon host: its daemon socket and its loopback ports.
type transport interface {
	// DialDaemon connects to the daemon, starting it (and whatever else the
	// host needs) when it is not running.
	DialDaemon() (net.Conn, error)
	// DialPort connects to 127.0.0.1:port on that host.
	DialPort(port int) (net.Conn, error)
	// Shell is a shell command on that host, for the caller to run.
	Shell(cmd string) *exec.Cmd
	// Tps is TPS's binary on that host, run with these (unquoted) arguments.
	Tps(args ...string) *exec.Cmd
	// Build is the build the daemon there should run: that of the binary
	// it gets installed (see BuildID).
	Build() string
}

type localTransport struct{}

func (localTransport) DialDaemon() (net.Conn, error) {
	sock := daemon.SocketPath()
	if conn, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		return conn, nil
	}
	if err := localPreflight(); err != nil {
		return nil, err
	}
	if err := startLocalDaemon(); err != nil {
		return nil, err
	}
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		if conn, err := net.DialTimeout("unix", sock, time.Second); err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("the daemon did not come up; see %s", daemonLog())
}

// unavailableError is a host that cannot run TPS as it is: the link says why,
// and waits for the user to try again rather than retrying by itself.
type unavailableError struct{ error }

// localPreflight refuses this machine where the daemon cannot run: other than
// on Linux, without podman or git, or with a podman that does not work.
func localPreflight() error {
	if runtime.GOOS != "linux" {
		return unavailableError{fmt.Errorf("TPS runs projects on Linux only; add a Linux host over SSH to run them there")}
	}
	var missing []string
	for _, tool := range []string{"podman", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return unavailableError{fmt.Errorf("this machine lacks %s; TPS needs podman and git to run projects here", strings.Join(missing, " and "))}
	}
	if out, err := exec.Command("podman", "info").CombinedOutput(); err != nil {
		return unavailableError{fmt.Errorf("podman does not work here (`podman info`: %v):\n%s", err, strings.TrimSpace(string(out)))}
	}
	return nil
}

func (localTransport) DialPort(port int) (net.Conn, error) {
	return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
}

func (localTransport) Shell(cmd string) *exec.Cmd { return exec.Command("sh", "-c", cmd) }

func (localTransport) Tps(args ...string) *exec.Cmd {
	exe, _ := os.Executable()
	return exec.Command(exe, args...)
}

func (localTransport) Build() string { return BuildID() }

func daemonLog() string {
	return filepath.Join(filepath.Dir(daemon.SocketPath()), "daemon.log")
}

// startLocalDaemon runs this same binary as a detached daemon, so it outlives the UI.
func startLocalDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(daemonLog()), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(daemonLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "--daemon")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it when it exits (a restart)
	return nil
}
