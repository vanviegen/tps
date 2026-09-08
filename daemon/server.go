package daemon

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vanviegen/agent-manager/hub"
)

// SocketPath is where the daemon listens, on every host.
func SocketPath() string {
	return filepath.Join(home(), ".local", "share", "tps", "daemon.sock")
}

// Serve runs the daemon until it is told to stop or restart. buildID is
// reported in the hello so that a UI can tell whether it runs a newer binary.
func Serve(buildID string) error {
	sock := SocketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		c.Close()
		return errors.New("a daemon is already running")
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	exit := func(code int) {
		ln.Close()
		_ = os.Remove(sock)
		os.Exit(code)
	}
	h := hub.New(map[string]any{"projects": map[string]any{}, "models": FallbackModels, "build": buildID, "protocol": hub.Protocol})
	m := NewManager(h, exit)
	h.Cmds = m.Cmds()
	if err := m.Start(); err != nil {
		return err
	}
	logf("TPS daemon listening on %s", sock)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		logf("Shutting down: stopping active workspaces…")
		go func() { time.Sleep(60 * time.Second); logf("Shutdown timed out"); exit(1) }()
		m.Shutdown()
		exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go serveConn(h, conn)
	}
}

// serveConn speaks the hub protocol as JSON lines over one connection.
func serveConn(h *hub.Hub, conn net.Conn) {
	client := h.AddClient()
	go func() {
		w := bufio.NewWriter(conn)
		for raw := range client.Out {
			if _, err := w.Write(append(raw, '\n')); err != nil {
				break
			}
			if len(client.Out) == 0 {
				if err := w.Flush(); err != nil {
					break
				}
			}
		}
		conn.Close()
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		h.Handle(client, append([]byte(nil), sc.Bytes()...))
	}
	h.RemoveClient(client)
	conn.Close()
}
