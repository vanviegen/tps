// Package sshx reaches a host through the user's own ssh binary, so that
// everything in their setup just works: ~/.ssh/config, agents, certificates,
// ProxyJump, and an already open multiplexed session. Any login prompt ssh
// raises (password, passphrase, unknown host key) is routed through
// SSH_ASKPASS back to the UI, which shows it in the browser.
package sshx

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RelayOK is written by `tps --relay` once it reached its socket, so a dial
// can tell a working relay from one that failed to connect.
const RelayOK = "TPS-RELAY-OK\n"

type Client struct {
	// Dest is the destination as the user gave it: what they would type after
	// `ssh`, so options like `-p 2222` or `-J jump` in front of the host work.
	Dest string
	// Env gives what every ssh process gets in its environment (the askpass
	// hookup), for a fresh run id per process.
	Env  func(run string) []string
	dest []string // Dest split into ssh arguments
	mux  []string // our own multiplexing options, when the user's config has none
}

// Host is the destination without the options in front: what to show the user.
func (c *Client) Host() string {
	if len(c.dest) == 0 {
		return c.Dest
	}
	return c.dest[len(c.dest)-1]
}

func New(dest string, env func(run string) []string) *Client {
	c := &Client{Dest: dest, Env: env, dest: strings.Fields(dest)}
	// Multiplex through the user's control socket when they have one
	// configured (their live session is then reused); otherwise keep a
	// master of our own so the host authenticates once, not per command.
	out, _ := exec.Command("ssh", append([]string{"-G"}, c.dest...)...).Output()
	if !controlPathSet(string(out)) {
		home, _ := os.UserHomeDir()
		dir := filepath.Join(home, ".local", "share", "tps", "ssh")
		_ = os.MkdirAll(dir, 0o700)
		c.mux = []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + dir + "/%C", "-o", "ControlPersist=60"}
	}
	return c
}

// controlPathSet reads `ssh -G` output; an unset ControlPath is printed as
// "none" or, depending on the version, not at all.
func controlPathSet(config string) bool {
	for _, line := range strings.Split(config, "\n") {
		if v, ok := strings.CutPrefix(line, "controlpath "); ok {
			return v != "none"
		}
	}
	return false
}

func (c *Client) command(args ...string) *exec.Cmd {
	base := []string{"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-o", "ConnectTimeout=20"}
	base = append(base, c.mux...)
	cmd := exec.Command("ssh", append(base, args...)...)
	run := make([]byte, 6)
	_, _ = rand.Read(run)
	cmd.Env = append(os.Environ(), c.Env(hex.EncodeToString(run))...)
	return cmd
}

type RunResult struct {
	Code     int
	Out, Err string
}

// Run executes a shell command on the host, feeding it stdin if given.
func (c *Client) Run(cmd string, stdin io.Reader) (RunResult, error) {
	p := c.command(append(append([]string{}, c.dest...), "--", cmd)...)
	p.Stdin = stdin
	var out, errb bytes.Buffer
	p.Stdout, p.Stderr = &out, &errb
	err := p.Run()
	r := RunResult{Out: out.String(), Err: errb.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		r.Code = exit.ExitCode()
		if r.Code == 255 { // ssh itself failed: no connection, no login
			return r, fmt.Errorf("ssh %s: %s", c.Dest, lastLine(r.Err))
		}
		return r, nil
	}
	return r, err
}

// Output runs a command that must succeed and returns its trimmed stdout.
func (c *Client) Output(cmd string) (string, error) {
	r, err := c.Run(cmd, nil)
	if err != nil {
		return "", err
	}
	if r.Code != 0 {
		return "", fmt.Errorf("`%s` on %s failed (%d): %s", cmd, c.Dest, r.Code, lastLine(r.Err+r.Out))
	}
	return strings.TrimSpace(r.Out), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// DialTCP connects to 127.0.0.1:port on the host (ssh -W).
func (c *Client) DialTCP(port int) (net.Conn, error) {
	return c.stdio(c.command(append([]string{"-W", fmt.Sprintf("127.0.0.1:%d", port)}, c.dest...)...), false)
}

// DialUnix connects to a unix socket on the host, through `tps --relay`
// running there (bin is the remote path of our binary).
func (c *Client) DialUnix(bin, path string) (net.Conn, error) {
	return c.stdio(c.command(append(append([]string{}, c.dest...), "--", bin+" --relay "+path)...), true)
}

// stdio turns an ssh process into a connection: its stdin and stdout are the
// two directions. With a handshake, the relay's RelayOK line must arrive
// before the connection counts as made.
func (c *Client) stdio(cmd *exec.Cmd, handshake bool) (net.Conn, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	conn := &procConn{cmd: cmd, in: stdin, out: stdout}
	if handshake {
		buf := make([]byte, len(RelayOK))
		if _, err := io.ReadFull(stdout, buf); err != nil || string(buf) != RelayOK {
			conn.Close()
			return nil, fmt.Errorf("ssh %s: %s", c.Dest, lastLine(errb.String()+"\nrelay failed"))
		}
	}
	return conn, nil
}

type procConn struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	once sync.Once
}

func (p *procConn) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *procConn) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *procConn) Close() error {
	p.once.Do(func() {
		p.in.Close()
		_ = p.cmd.Process.Kill()
		go p.cmd.Wait()
	})
	return nil
}
func (p *procConn) LocalAddr() net.Addr                { return sshAddr("local") }
func (p *procConn) RemoteAddr() net.Addr               { return sshAddr("ssh") }
func (p *procConn) SetDeadline(t time.Time) error      { return nil }
func (p *procConn) SetReadDeadline(t time.Time) error  { return nil }
func (p *procConn) SetWriteDeadline(t time.Time) error { return nil }

type sshAddr string

func (a sshAddr) Network() string { return "ssh" }
func (a sshAddr) String() string  { return string(a) }
