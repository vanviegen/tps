package ui

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vanviegen/tps/daemon"
)

// A host's claude login is made here, by the dashboard, because this is the
// end the browser talks to. `claude auth login` tries to open a browser tab
// that hands the code back on localhost, which only works with both on one
// machine; it also prints a page to visit that shows the code instead, for
// pasting back. The dashboard offers that page and takes the code, so a
// dashboard without a browser beside it (in a container, say) signs in too.
// What claude writes goes to the host's daemon, which keeps a login of its own
// (see daemon/login.go), and nothing of it stays here. The host's "signin"
// says how that is going, while it is, or how it failed; "signingIn" and
// "signinURL" that it is, and its page once claude has told.

const signInTimeout = 5 * time.Minute // a tab closed without authorising ends as a failure to try again

// signIn starts a sign-in for the host, unless one is under way.
func (u *UI) signIn(l *Link) error {
	l.mu.Lock()
	busy := l.signinCode != nil
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
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+dir, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+dir) // the latter inherited in a task
	stdin, _ := cmd.StdinPipe()                                                                      // held open: claude would take its end as a code pasted
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return err
	}
	l.mu.Lock()
	l.signinCode = stdin
	l.mu.Unlock()
	l.setSignIn("signing in to claude: finish it on its sign-in page", "")
	go func() {
		stop := time.AfterFunc(signInTimeout, func() { _ = cmd.Process.Kill() })
		last := ""
		lines := bufio.NewScanner(out)
		for lines.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(lines.Text(), "Paste code here if prompted >"))
			if _, url, ok := strings.Cut(line, "visit: "); ok {
				l.setSignIn("signing in to claude: finish it on its sign-in page", url)
			}
			if line != "" {
				last = line
			}
		}
		err := cmd.Wait()
		stop.Stop()
		l.mu.Lock()
		l.signinCode = nil
		l.mu.Unlock()
		stdin.Close()
		credentials, readErr := os.ReadFile(filepath.Join(dir, ".credentials.json"))
		os.RemoveAll(dir)
		switch {
		case err != nil: // claude's last line says why
			l.setSignIn("the sign-in failed ("+last+"): try again", "")
		case readErr != nil:
			l.setSignIn("the sign-in failed (claude left no credentials behind): try again", "")
		default:
			_, err := l.cmd("setLogin", map[string]any{"text": string(credentials)})
			if err != nil {
				l.setSignIn("the sign-in failed ("+err.Error()+"): try again", "")
			} else {
				l.setSignIn("", "")
			}
		}
	}()
	return nil
}

func (l *Link) setSignIn(state, url string) {
	l.mu.Lock()
	l.signin, l.signinURL = state, url
	l.mu.Unlock()
	l.publishHost()
}

func (u *UI) login(raw json.RawMessage) (any, error) {
	return u.hostCmd(raw, u.signIn)
}

// signInCode hands claude the code its sign-in page showed.
func (u *UI) signInCode(raw json.RawMessage) (any, error) {
	var args struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &args)
	return u.hostCmd(raw, func(l *Link) error {
		l.mu.Lock()
		code := l.signinCode
		l.mu.Unlock()
		if code == nil {
			return errors.New("no sign-in is under way")
		}
		_, err := io.WriteString(code, strings.TrimSpace(args.Code)+"\n")
		return err
	})
}
