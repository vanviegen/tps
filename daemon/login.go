package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// The daemon has a claude directory of its own, and every claude it runs
// shares the login in it: the errands on the host (naming a task, asking for
// the model list) use it as their config dir outright, and each task's
// container gets it mounted as the place claude keeps its credentials, with
// the task's own config dir staying private (see ensureContainer). One login
// per host, then, in one place: a refresh revokes the token it used, so
// copies would log each other out, and claude serialises refreshes with a
// lock it keeps beside the credentials, which only works when that is one
// directory for everyone.
//
// The login itself is made for the dashboard by SignIn (see ui/login.go), and
// handed over with setLogin. The daemon only says whether it has one, under
// "login": nothing while it does, else why not.

// authDir is that directory, made on first use (it is a mount source, so it
// has to exist) and resolved: a TPS running as a task in another TPS gets it
// as a link to the login it inherits (see demo/seed.sh), and podman needs the
// real path.
func authDir() string {
	dir := filepath.Join(DataDir(), "claude")
	_ = os.MkdirAll(dir, 0o700)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// claudeEnv is what a claude run by the daemon itself gets: the credentials
// dir named too, as a TPS running in a task inherits one pointing elsewhere.
func claudeEnv() []string {
	return []string{"CLAUDE_CONFIG_DIR=" + authDir(), "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + authDir()}
}

func credentialsFile() string { return filepath.Join(authDir(), ".credentials.json") } // where claude keeps a login

// noLogin is what is said wherever a missing or rejected login stops claude:
// the host's state, a task's chat, a review that did not run. The dashboard
// puts the button that signs in beside it.
const noLogin = "Claude needs signing in on this host"

// publishLogin tells the dashboards whether this host has a login to run on.
// A login that is there but no longer accepted counts as none: the dashboards
// show the same warning and the same button, which is the whole of what the
// user has to do about it.
func (m *Manager) publishLogin() {
	var state any
	if m.loginGone.Load() || !exists(credentialsFile()) && os.Getenv("ANTHROPIC_API_KEY") == "" {
		state = noLogin
	}
	m.hub.Set([]string{"login"}, state)
}

// loginExpired: a turn failed because claude could not authenticate (see
// authGone). Nothing but a run finds that out — the credentials are still
// there, and only claude knows they are stale — so the word comes from the
// task that ran into it, and from here the whole dashboard asks for a sign-in.
func (m *Manager) loginExpired() {
	if m.loginGone.Swap(true) {
		return
	}
	logf("claude's login is no longer accepted; asking for a new one")
	m.publishLogin()
}

// setLogin takes the credentials the dashboard signed in for, as claude wrote
// them, and makes them this host's.
func (m *Manager) setLogin(credentials string) error {
	if err := os.WriteFile(credentialsFile(), []byte(credentials), 0o600); err != nil {
		return err
	}
	logf("signed in to claude")
	m.loginGone.Store(false)
	m.publishLogin()
	go m.refreshModels() // there is a login to ask with now
	return nil
}

// CredentialsMark leads the line SignIn ends with.
const CredentialsMark = "TPS-CREDENTIALS "

// SignIn is `tps --sign-in`: claude's sign-in, on this process's stdio, into a
// directory of its own that is gone afterwards. The end of stdin ends it: the
// dashboard closes it to give up, and so does its going, or that of the SSH
// connection it runs this over. Once it succeeded, the login it made follows
// on a line of its own after CredentialsMark, for the dashboard to hand to the
// host it signs in; a failure ends with a line that says why. Returns the exit
// code. The toolbox is fetched for it, as for a task.
func SignIn() int {
	fail := func(err error) int {
		fmt.Println(err)
		return 1
	}
	if _, err := ensureToolbox(); err != nil {
		return fail(err)
	}
	dir, err := os.MkdirTemp("", "tps-login-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command(claudeBin(), "auth", "login")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+dir, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+dir) // the latter inherited in a task
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stdout
	in, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return fail(err)
	}
	go func() {
		_, _ = io.Copy(in, os.Stdin) // claude itself waits on past the end of its input
		_ = cmd.Process.Kill()
	}()
	if err := cmd.Wait(); err != nil {
		return 1 // claude's last line says why
	}
	credentials, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return fail(errors.New("claude left no credentials behind"))
	}
	var line bytes.Buffer
	if err := json.Compact(&line, credentials); err != nil {
		return fail(err)
	}
	fmt.Println(CredentialsMark + line.String())
	return 0
}
