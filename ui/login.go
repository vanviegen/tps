package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vanviegen/tps/daemon"
)

// A host's claude login is made here, on the dashboard's machine, because
// this is where the browser is: `claude auth login` opens it and takes the
// code back on localhost, which only works with both on one machine. What it
// writes goes to the host's daemon, which keeps a login of its own (see
// daemon/login.go), and nothing of it stays here. The host's "signin" says
// how that is going, while it is, or how it failed.

const signInTimeout = 5 * time.Minute // a tab closed without authorising ends as a failure to try again

// signIn starts a sign-in for the host, unless one is under way.
func (u *UI) signIn(l *Link) error {
	l.mu.Lock()
	busy := strings.HasPrefix(l.signin, "signing in")
	l.mu.Unlock()
	if busy {
		return nil
	}
	bin, err := daemon.ClaudeBin()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "tps-login-")
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "auth", "login")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+dir)
	stdin, _ := cmd.StdinPipe() // held open: claude would take its end as a code pasted
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return err
	}
	l.setSignIn("signing in: finish it in the browser tab that opened")
	go func() {
		defer stdin.Close()
		stop := time.AfterFunc(signInTimeout, func() { _ = cmd.Process.Kill() })
		err := cmd.Wait()
		stop.Stop()
		credentials, readErr := os.ReadFile(filepath.Join(dir, ".credentials.json"))
		os.RemoveAll(dir)
		switch {
		case err != nil: // claude's last line says why
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			l.setSignIn("the sign-in failed (" + strings.TrimSpace(strings.TrimPrefix(lines[len(lines)-1], "Paste code here if prompted >")) + "): try again")
		case readErr != nil:
			l.setSignIn("the sign-in failed (claude left no credentials behind): try again")
		default:
			_, err := l.cmd("setLogin", map[string]any{"text": string(credentials)})
			if err != nil {
				l.setSignIn("the sign-in failed (" + err.Error() + "): try again")
			} else {
				l.setSignIn("")
			}
		}
	}()
	return nil
}

func (l *Link) setSignIn(state string) {
	l.mu.Lock()
	l.signin = state
	l.mu.Unlock()
	l.publishHost()
}

func (u *UI) login(raw json.RawMessage) (any, error) {
	return u.hostCmd(raw, u.signIn)
}
