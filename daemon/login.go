package daemon

import (
	"os"
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
// The login itself is made by the dashboard, on the machine the browser is
// on (see ui/login.go), and handed over with setLogin. The daemon only says
// whether it has one, under "login": nothing while it does, else why not.

// authDir is that directory, made on first use (it is a mount source, so it
// has to exist) and resolved: a TPS running as a task in another TPS gets it
// as a link to the login it inherits (see demo/seed.sh), and podman needs the
// real path.
func authDir() string {
	dir := filepath.Join(home(), ".local", "share", "tps", "claude")
	_ = os.MkdirAll(dir, 0o700)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// claudeEnv is what a claude run by the daemon itself gets.
func claudeEnv() []string {
	return []string{"CLAUDE_CONFIG_DIR=" + authDir()}
}

func credentialsFile() string { return filepath.Join(authDir(), ".credentials.json") } // where claude keeps a login

// publishLogin tells the dashboards whether this host has a login to run on.
func (m *Manager) publishLogin() {
	var state any
	if !exists(credentialsFile()) && os.Getenv("ANTHROPIC_API_KEY") == "" {
		state = "not signed in to claude: its agents cannot run until you sign in"
	}
	m.hub.Set([]string{"login"}, state)
}

// setLogin takes the credentials the dashboard signed in for, as claude wrote
// them, and makes them this host's.
func (m *Manager) setLogin(credentials string) error {
	if err := os.WriteFile(credentialsFile(), []byte(credentials), 0o600); err != nil {
		return err
	}
	logf("signed in to claude")
	m.publishLogin()
	go m.refreshModels() // there is a login to ask with now
	return nil
}
