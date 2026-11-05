package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// User text: a note the container puts in front of the user, at the bottom of
// the task's chat. '/tps/guest-tool show-user-text <text>' writes it to
// userTextFile in the services dir, replacing the one before, and without a
// text removes it; the daemon reads it on its services tick and publishes it.
// It lasts as long as the container does (see resetServices).

const userTextFile = ".user-text"

func (st *guestTool) showUserText(args []string) error {
	file := filepath.Join(st.root, userTextFile)
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "" {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(file, []byte(text), 0o644)
}

// syncUserTextL publishes the text the container shows the user, if any.
func (t *Task) syncUserTextL() {
	t.pubL("userText", nonEmpty(readFile(filepath.Join(t.servicesDir(), userTextFile))))
}
