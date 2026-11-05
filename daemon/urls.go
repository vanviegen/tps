package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// URLs: pages a task's container puts in front of the user, each a tab beside
// VS Code in the dashboard. '/tps/guest-tool set-url <id> <url>' writes the URL
// to urlsDir/<id> in the services dir, and one without a URL takes it away; the
// daemon reads them on its services tick and publishes them. They last as
// long as the container does (see resetServices). A URL set while nobody has
// the task open flags it until someone does (urlsNew), and the dashboard
// switches to a tab whose URL is set while it is shown.

const urlsDir = ".urls"

// taskURL is one tab's page. At, the file's mtime in ms, tells the dashboard
// a URL set anew from one it has shown already.
type taskURL struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	At  int64  `json:"at"`
}

// webURL: only http(s) is opened in the dashboard, whose frame would run a
// javascript: URL in the dashboard itself.
func webURL(url string) bool {
	url = strings.ToLower(url)
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

// --- the tool's side ---

func (st *guestTool) setURL(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return errors.New("set-url: give an id, and a URL (none to remove it)")
	}
	id, url := args[0], ""
	if len(args) == 2 {
		url = strings.TrimSpace(args[1])
	}
	if !serviceNameRe.MatchString(id) {
		return fmt.Errorf("'%s' is not an id (letters, digits, . _ -; up to 40)", id)
	}
	file := filepath.Join(st.root, urlsDir, id)
	if url == "" {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if !webURL(url) {
		return fmt.Errorf("'%s' is not an http(s) URL", url)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return writeAtomic(file, []byte(url), 0o644)
}

// --- the daemon's side ---

// readURLs reads the URLs set in a services dir, by id.
func readURLs(servicesDir string) []taskURL {
	dir := filepath.Join(servicesDir, urlsDir)
	entries, _ := os.ReadDir(dir)
	var urls []taskURL
	for _, e := range entries {
		info, err := e.Info()
		url := strings.TrimSpace(readFile(filepath.Join(dir, e.Name())))
		if err != nil || !serviceNameRe.MatchString(e.Name()) || !webURL(url) {
			continue
		}
		urls = append(urls, taskURL{e.Name(), url, info.ModTime().UnixMilli()})
	}
	sort.Slice(urls, func(i, j int) bool { return urls[i].ID < urls[j].ID })
	return urls
}

// syncURLsL reads the task's URLs and publishes them, flagging the task where
// one is set anew while nobody has it open. Those a restarted daemon finds on
// its first read are not new.
func (t *Task) syncURLsL() {
	urls := readURLs(t.servicesDir())
	known := map[taskURL]bool{}
	for _, u := range t.urls {
		known[u] = true
	}
	for _, u := range urls {
		if t.urlsRead && !known[u] && t.viewers == 0 && !t.urlsNew {
			t.urlsNew = true
			t.pubL("urlsNew", true)
		}
	}
	t.urls, t.urlsRead = urls, true
	if len(urls) == 0 {
		t.pubL("urls", nil)
	} else {
		t.pubL("urls", urls)
	}
}

// seenURLsL clears the flag: someone has the task open.
func (t *Task) seenURLsL() {
	if t.urlsNew {
		t.urlsNew = false
		t.pubL("urlsNew", nil)
	}
}
