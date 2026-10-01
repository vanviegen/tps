package ui

import (
	"strings"
	"testing"

	"github.com/vanviegen/tps/daemon"
	"github.com/vanviegen/tps/hub"
)

// uiHandled are the daemon's commands the dashboard does not simply forward:
// the ones that are about a host rather than a project on it, and so have a
// handler of their own (see registerCmds).
var uiHandled = map[string]bool{
	"addProject":       true, // needs the host picked before there is a pid to translate
	"restart":          true, // the daemon's own lifecycle: stopDaemon and updateDaemon drive it
	"stop":             true,
	"setLogin":         true, // the login is made in the browser and handed over (see ui/login.go)
	"setIdentity":      true, // this machine's git identity, handed over on connecting (see handOverIdentity)
	"setOpenRouterKey": true, // set on the host's page (see setOpenRouterKey)
}

// The identity handed to a host is who git here would commit as, which is more
// than what a config file holds — and nothing at all when git cannot say.
func TestGitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Ada L")
	t.Setenv("GIT_AUTHOR_EMAIL", "ada@example.com")
	if name, email := gitIdentity(); name != "Ada L" || email != "ada@example.com" {
		t.Errorf("the environment's identity came back as %q <%s>", name, email)
	}
	t.Setenv("PATH", t.TempDir()) // no git on this machine
	if name, email := gitIdentity(); name != "" || email != "" {
		t.Errorf("without git, the identity came back as %q <%s>", name, email)
	}
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

// A login ssh asks for is put to the user as a question on its host, and
// answering it is what makes the next attempt: ssh is never left waiting on a
// human, the answer is handed to the processes that follow, and one they
// refuse is asked over again.
func TestSSHLoginWaitsOnTheHost(t *testing.T) {
	u := &UI{hub: hub.New(map[string]any{"hosts": map[string]any{}}), links: map[string]*Link{}}
	u.askpass = &askpassServer{secrets: map[string]string{}, given: map[string]string{}}
	l := newLink(u, "h1", "me@host", nil)
	u.links["h1"] = l
	prompt := AskpassRequest{Host: "h1", Run: "r1", Prompt: "me@host's password:"}

	if reply := u.askpass.answer(u, prompt); !reply.Cancel {
		t.Fatal("ssh was left waiting for the user instead of being turned down")
	}
	ask, _ := u.hub.Get("hosts", "h1", "ask").(map[string]any)
	if ask["kind"] != "password" {
		t.Fatalf("the host does not say it needs a login: %v", u.hub.Get("hosts", "h1"))
	}
	if _, err := u.answer([]byte(`{"hid":"h1","value":"hunter2"}`)); err != nil {
		t.Fatal(err)
	}

	prompt.Run = "r2"
	if reply := u.askpass.answer(u, prompt); reply.Answer != "hunter2" {
		t.Fatalf("the next ssh process was not given the answer: %+v", reply)
	}
	// The same process asking again means that answer was refused.
	if reply := u.askpass.answer(u, prompt); !reply.Cancel {
		t.Fatalf("a refused answer was handed out a second time: %+v", reply)
	}
	ask, _ = u.hub.Get("hosts", "h1", "ask").(map[string]any)
	if text, _ := ask["text"].(string); !strings.HasPrefix(text, "That did not work.") {
		t.Errorf("the host does not say the login was refused: %q", text)
	}
	prompt.Run = "r3"
	if reply := u.askpass.answer(u, prompt); !reply.Cancel {
		t.Errorf("the refused answer survived into the next ssh process: %+v", reply)
	}
}
