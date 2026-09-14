package ui

import (
	"testing"

	"github.com/vanviegen/tps/daemon"
	"github.com/vanviegen/tps/hub"
)

// uiHandled are the daemon's commands the dashboard does not simply forward:
// the ones that are about a host rather than a project on it, and so have a
// handler of their own (see registerCmds).
var uiHandled = map[string]bool{
	"addProject": true, // needs the host picked before there is a pid to translate
	"restart":    true, // the daemon's own lifecycle: stopDaemon and updateDaemon drive it
	"stop":       true,
	"setLogin":   true, // the login is made in the browser and handed over (see ui/login.go)
}

// Every command the daemon answers has to be reachable: a dashboard forwards
// the ones on its list and nothing else, so one added to the daemon and not to
// the list is a button that reports "unknown command" and nothing more.
func TestEveryDaemonCommandIsReachable(t *testing.T) {
	forwarded := map[string]bool{}
	for _, name := range daemonCmds {
		forwarded[name] = true
	}
	m := daemon.NewManager(hub.New(nil), func(int) {})
	for name := range m.Cmds() {
		if !forwarded[name] && !uiHandled[name] {
			t.Errorf("the daemon answers %q, but no dashboard can ask it: add it to daemonCmds", name)
		}
	}
	for name := range forwarded {
		if m.Cmds()[name] == nil {
			t.Errorf("dashboards forward %q, which the daemon does not answer", name)
		}
	}
}
