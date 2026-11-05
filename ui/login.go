package ui

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/vanviegen/tps/daemon"
)

// A host's claude login is made by `claude auth login`, run by `tps --sign-in`
// (see daemon.SignIn). That tries to open a browser tab that hands the code
// back on localhost, which only works with both on one machine; it also
// prints a page to visit that shows the code instead, for pasting back. The
// dashboard offers that page and takes the code, so a dashboard without a
// browser beside it (in a container, say) signs in too. On Linux the sign-in
// runs here, where the browser is; elsewhere the host runs it, as there is no
// claude for this machine in the toolbox. What claude wrote goes to the
// host's daemon, which keeps a login of its own (see daemon/login.go), and
// nothing of it stays behind. The host's "signin" says how that is going,
// while it is, or how it failed; "signingIn" and "signinURL" that it is, and
// its page once claude has told.

const signInTimeout = 5 * time.Minute // a tab closed without authorising ends as a failure to try again

// signIn starts a sign-in for the host, unless one is under way.
func (u *UI) signIn(l *Link) error {
	l.mu.Lock()
	busy := l.signinCode != nil
	l.mu.Unlock()
	if busy {
		return nil
	}
	tr := l.tr
	if runtime.GOOS == "linux" {
		tr = localTransport{}
	}
	cmd := tr.Tps("--sign-in")
	stdin, _ := cmd.StdinPipe() // held open: its end ends the sign-in
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	l.mu.Lock()
	l.signinCode = stdin
	l.mu.Unlock()
	l.setSignIn("signing in to claude: finish it on its sign-in page", "")
	go func() {
		stop := time.AfterFunc(signInTimeout, func() { stdin.Close() })
		last, credentials := "", ""
		lines := bufio.NewScanner(out)
		for lines.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(lines.Text(), "Paste code here if prompted >"))
			if _, url, ok := strings.Cut(line, "visit: "); ok {
				l.setSignIn("signing in to claude: finish it on its sign-in page", url)
			}
			if c, ok := strings.CutPrefix(line, daemon.CredentialsMark); ok {
				credentials = c
			} else if line != "" {
				last = line
			}
		}
		err := cmd.Wait()
		stop.Stop()
		l.mu.Lock()
		l.signinCode = nil
		l.mu.Unlock()
		stdin.Close()
		if err != nil || credentials == "" { // the last line says why
			l.setSignIn("the sign-in failed ("+last+"): try again", "")
			return
		}
		if _, err := l.cmd("setLogin", map[string]any{"text": credentials}); err != nil {
			l.setSignIn("the sign-in failed ("+err.Error()+"): try again", "")
		} else {
			l.setSignIn("", "")
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
