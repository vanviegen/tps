package ui

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
}

type localTransport struct{}

func (localTransport) DialDaemon() (net.Conn, error) {
	sock := daemon.SocketPath()
	if conn, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		return conn, nil
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

func (localTransport) DialPort(port int) (net.Conn, error) {
	return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
}

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
