// Package sshx is the UI's SSH client: it resolves a destination the way ssh
// does (~/.ssh/config, agent, identity files, known_hosts), asks the user for
// passwords and passphrases through a prompter, and keeps one connection per
// host alive for exec, unix-socket and loopback dialing.
package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Prompter asks the user a question of kind "password" (hidden input),
// "text", or "confirm" (yes/no; any answer without error is a yes). An error
// means the user declined.
type Prompter func(title, text, kind string) (string, error)

type Client struct {
	Dest   string
	prompt Prompter

	mu          sync.Mutex
	client      *ssh.Client
	password    string
	passphrases map[string]string
}

func New(dest string, prompt Prompter) *Client {
	return &Client{Dest: dest, prompt: prompt, passphrases: map[string]string{}}
}

type target struct {
	user, host, port string
	identityFiles    []string
}

// sshConfigs loads ~/.ssh/config and the system config, in that order.
func sshConfigs() []*ssh_config.Config {
	var cfgs []*ssh_config.Config
	home, _ := os.UserHomeDir()
	for _, file := range []string{filepath.Join(home, ".ssh", "config"), "/etc/ssh/ssh_config"} {
		if f, err := os.Open(file); err == nil {
			if cfg, err := ssh_config.Decode(f); err == nil {
				cfgs = append(cfgs, cfg)
			}
			f.Close()
		}
	}
	return cfgs
}

func cfgGet(cfgs []*ssh_config.Config, alias, key string) string {
	for _, cfg := range cfgs {
		if v, _ := cfg.Get(alias, key); v != "" {
			return v
		}
	}
	return ""
}

// resolve applies the ssh config to a [user@]host[:port] destination.
func resolve(dest string) (target, error) {
	t := target{port: "22"}
	rest := dest
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		t.user, rest = rest[:i], rest[i+1:]
	}
	if h, p, err := net.SplitHostPort(rest); err == nil {
		rest, t.port = h, p
	}
	if rest == "" {
		return t, errors.New("empty host")
	}
	cfgs := sshConfigs()
	alias := rest
	t.host = alias
	if v := cfgGet(cfgs, alias, "HostName"); v != "" {
		t.host = v
	}
	if t.user == "" {
		if v := cfgGet(cfgs, alias, "User"); v != "" {
			t.user = v
		} else if u, err := user.Current(); err == nil {
			t.user = u.Username
		}
	}
	if v := cfgGet(cfgs, alias, "Port"); v != "" && t.port == "22" {
		t.port = v
	}
	var files []string
	for _, cfg := range cfgs {
		if v, _ := cfg.GetAll(alias, "IdentityFile"); len(v) > 0 {
			files = v
			break
		}
	}
	if len(files) == 0 {
		files = []string{"~/.ssh/id_ed25519", "~/.ssh/id_ecdsa", "~/.ssh/id_rsa"}
	}
	home, _ := os.UserHomeDir()
	for _, f := range files {
		if strings.HasPrefix(f, "~/") {
			f = filepath.Join(home, f[2:])
		}
		if _, err := os.Stat(f); err == nil {
			t.identityFiles = append(t.identityFiles, f)
		}
	}
	return t, nil
}

// Connect returns the live connection, dialing (and prompting) when there is none.
func (c *Client) Connect() (*ssh.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	t, err := resolve(c.Dest)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User:            t.user,
		Auth:            c.authMethods(t),
		HostKeyCallback: c.hostKeyCallback(),
		Timeout:         20 * time.Second,
	}
	addr := net.JoinHostPort(t.host, t.port)
	conn, err := net.DialTimeout("tcp", addr, config.Timeout)
	if err != nil {
		return nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	client := ssh.NewClient(cc, chans, reqs)
	c.client = client
	go c.keepalive(client)
	return client, nil
}

func (c *Client) authMethods(t target) []ssh.AuthMethod {
	// One publickey method holding every key: the ssh client tries each
	// method name once, so agent and file keys must share it.
	keys := ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		var signers []ssh.Signer
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				if s, err := agent.NewClient(conn).Signers(); err == nil {
					signers = append(signers, s...)
				}
			}
		}
		for _, file := range t.identityFiles {
			if pub, err := os.ReadFile(file + ".pub"); err == nil {
				if key, _, _, _, err := ssh.ParseAuthorizedKey(pub); err == nil && hasKey(signers, key) {
					continue // the agent already offers it; no passphrase needed
				}
			}
			if s := c.loadKey(file); s != nil && !hasKey(signers, s.PublicKey()) {
				signers = append(signers, s)
			}
		}
		return signers, nil
	})
	methods := []ssh.AuthMethod{keys}
	attempts := 0
	askPassword := func() (string, error) {
		attempts++
		if attempts == 1 && c.password != "" {
			return c.password, nil
		}
		text := "Password for " + c.Dest
		if attempts > 1 {
			text = "Wrong password. " + text
		}
		pw, err := c.prompt("SSH login", text, "password")
		if err != nil {
			return "", err
		}
		c.password = pw
		return pw, nil
	}
	methods = append(methods,
		ssh.RetryableAuthMethod(ssh.PasswordCallback(askPassword), 3),
		ssh.RetryableAuthMethod(ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i, q := range questions {
				if strings.Contains(strings.ToLower(q), "password") {
					pw, err := askPassword()
					if err != nil {
						return nil, err
					}
					answers[i] = pw
					continue
				}
				text := strings.TrimSpace(instruction + "\n" + q)
				kind := "password"
				if echos[i] {
					kind = "text"
				}
				a, err := c.prompt("SSH login to "+c.Dest, text, kind)
				if err != nil {
					return nil, err
				}
				answers[i] = a
			}
			return answers, nil
		}), 3),
	)
	return methods
}

func hasKey(signers []ssh.Signer, key ssh.PublicKey) bool {
	for _, s := range signers {
		if bytes.Equal(s.PublicKey().Marshal(), key.Marshal()) {
			return true
		}
	}
	return false
}

// loadKey parses an identity file, asking for its passphrase when encrypted.
func (c *Client) loadKey(file string) ssh.Signer {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return signer
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		pass := c.passphrases[file]
		if pass == "" || attempt > 0 {
			text := "Passphrase for " + file
			if attempt > 0 {
				text = "Wrong passphrase. " + text
			}
			pass, err = c.prompt("SSH key", text, "password")
			if err != nil {
				return nil
			}
		}
		if signer, err := ssh.ParsePrivateKeyWithPassphrase(data, []byte(pass)); err == nil {
			c.passphrases[file] = pass
			return signer
		}
	}
	return nil
}

// hostKeyCallback checks ~/.ssh/known_hosts; an unknown host is confirmed by
// the user and remembered, a changed key is refused.
func (c *Client) hostKeyCallback() ssh.HostKeyCallback {
	home, _ := os.UserHomeDir()
	file := filepath.Join(home, ".ssh", "known_hosts")
	return func(hostport string, remote net.Addr, key ssh.PublicKey) error {
		_ = os.MkdirAll(filepath.Dir(file), 0o700)
		if f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.Close()
		}
		check, err := knownhosts.New(file)
		if err != nil {
			return err
		}
		err = check(hostport, remote, key)
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err
		}
		if len(keyErr.Want) > 0 {
			return fmt.Errorf("the host key of %s has changed (%s); fix ~/.ssh/known_hosts if that is expected", hostport, ssh.FingerprintSHA256(key))
		}
		if _, err := c.prompt("Unknown host "+c.Dest,
			fmt.Sprintf("The authenticity of %s can't be established. Its %s key fingerprint is %s. Connect anyway?", hostport, key.Type(), ssh.FingerprintSHA256(key)), "confirm"); err != nil {
			return errors.New("host key not accepted")
		}
		f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostport)}, key))
		return err
	}
}

// keepalive pings until the connection fails, then forgets it so the next Connect redials.
func (c *Client) keepalive(client *ssh.Client) {
	for {
		time.Sleep(15 * time.Second)
		done := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()
		var err error
		select {
		case err = <-done:
		case <-time.After(30 * time.Second):
			err = errors.New("keepalive timeout")
		}
		if err != nil {
			c.drop(client)
			return
		}
	}
}

func (c *Client) drop(client *ssh.Client) {
	c.mu.Lock()
	if c.client == client {
		c.client = nil
	}
	c.mu.Unlock()
	client.Close()
}

// Dial opens a connection on the remote host: "tcp" to an address there, or
// "unix" to a socket path. A connection found dead is redialed once.
func (c *Client) Dial(network, addr string) (net.Conn, error) {
	for attempt := 0; ; attempt++ {
		client, err := c.Connect()
		if err != nil {
			return nil, err
		}
		conn, err := client.Dial(network, addr)
		if err != nil && isConnErr(err) && attempt == 0 {
			c.drop(client)
			continue
		}
		return conn, err
	}
}

func isConnErr(err error) bool {
	return errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed")
}

type RunResult struct {
	Code     int
	Out, Err string
}

// Run executes a shell command on the host, feeding it stdin if given.
func (c *Client) Run(cmd string, stdin io.Reader) (RunResult, error) {
	var session *ssh.Session
	for attempt := 0; session == nil; attempt++ {
		client, err := c.Connect()
		if err != nil {
			return RunResult{}, err
		}
		session, err = client.NewSession()
		if err != nil {
			if isConnErr(err) && attempt == 0 {
				c.drop(client)
				continue
			}
			return RunResult{}, err
		}
	}
	defer session.Close()
	var out, errb bytes.Buffer
	session.Stdin, session.Stdout, session.Stderr = stdin, &out, &errb
	err := session.Run(cmd)
	r := RunResult{Out: out.String(), Err: errb.String()}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		r.Code = exit.ExitStatus()
		return r, nil
	}
	if err != nil {
		r.Code = -1
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
		msg := strings.TrimSpace(r.Err)
		if msg == "" {
			msg = strings.TrimSpace(r.Out)
		}
		return "", fmt.Errorf("`%s` failed (%d): %s", cmd, r.Code, msg)
	}
	return strings.TrimSpace(r.Out), nil
}

func (c *Client) Close() {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()
	if client != nil {
		client.Close()
	}
}
