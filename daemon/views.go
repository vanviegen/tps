package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Views: a page the container asks the user to look at, on one of its ports.
// '/tps/guest-tool request-human-views <port> [path]' writes the port and path
// to viewFile in the services dir, the newest request replacing the one
// before, and without arguments removes it; the daemon reads it on its
// services tick and publishes it. The dashboard says so at the bottom of the
// chat, and switches to the port's tab once it answers. The request lasts as long as the container does (see
// resetServices). One made while nobody has the task open flags it until
// someone does (viewNew).

const viewFile = ".view"

// viewRequest is the page asked for. At, the file's mtime in ms, tells the
// dashboard a request made anew from one it has shown already.
type viewRequest struct {
	Port int    `json:"port"`
	Path string `json:"path"`
	At   int64  `json:"at"`
}

// --- the tool's side ---

func (st *guestTool) requestViews(args []string) error {
	file := filepath.Join(st.root, viewFile)
	if len(args) == 0 {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if len(args) > 2 {
		return errors.New("request-human-views: give a port, and a path (/ by default), or nothing to withdraw the request")
	}
	port, err := strconv.Atoi(args[0])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("'%s' is not a port", args[0])
	}
	path := "/"
	if len(args) == 2 {
		path = "/" + strings.TrimPrefix(strings.TrimSpace(args[1]), "/")
	}
	return writeAtomic(file, fmt.Appendf(nil, "%d %s", port, path), 0o644)
}

// --- the daemon's side ---

// readView reads the request made in a services dir, if any.
func readView(servicesDir string) *viewRequest {
	file := filepath.Join(servicesDir, viewFile)
	info, err := os.Stat(file)
	if err != nil {
		return nil
	}
	portText, path, _ := strings.Cut(strings.TrimSpace(readFile(file)), " ")
	port, err := strconv.Atoi(portText)
	if err != nil || !strings.HasPrefix(path, "/") {
		return nil
	}
	return &viewRequest{port, path, info.ModTime().UnixMilli()}
}

// syncViewL reads the task's view request and publishes it, flagging the task
// where one is made anew while nobody has it open, until it is withdrawn. One
// a restarted daemon finds on its first read is not new.
func (t *Task) syncViewL() {
	v := readView(t.servicesDir())
	if v != nil && t.viewRead && (t.view == nil || *v != *t.view) && t.viewers == 0 && !t.viewNew {
		t.viewNew = true
		t.pubL("viewNew", true)
	}
	t.view, t.viewRead = v, true
	if v == nil {
		t.seenViewL()
		t.pubL("view", nil)
	} else {
		t.pubL("view", v)
	}
}

// seenViewL clears the flag: someone has the task open.
func (t *Task) seenViewL() {
	if t.viewNew {
		t.viewNew = false
		t.pubL("viewNew", nil)
	}
}
