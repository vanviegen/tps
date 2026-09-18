package daemon

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Attachments: files a user adds to a chat message or to a plan's description
// — a screenshot pasted into the dashboard, most often. They are kept per
// task, in a directory mounted read-only into its container, and the text
// refers to them by the path the agent reads them at, so nothing but text ever
// reaches claude.

// uploadsMount is where a task's attachments are in its container.
const uploadsMount = "/uploads"

const (
	maxUploadBytes  = 8 << 20  // one attachment
	maxMessageBytes = 10 << 20 // all attachments of one message; base64 of that still fits the websocket's 16 MB
)

// ChatFile is one attachment as a dashboard sends it: the name it picked (and
// wrote into the message text) and the bytes, base64 as JSON carries no others.
type ChatFile struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

func (t *Task) uploadsDir() string { return filepath.Join(t.dir(), "uploads") }

// storeUploads writes attachments to the task, each under the first name like
// the one asked for that is free here (an earlier message, or another
// dashboard, may have taken it), and returns the names they got. Nothing is
// written unless everything can be: files arrive together or not at all.
func (t *Task) storeUploads(files []ChatFile) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	raws := make([][]byte, len(files))
	total := 0
	for i, f := range files {
		raw, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return nil, fmt.Errorf("attachment %q could not be read", f.Name)
		}
		if len(raw) > maxUploadBytes {
			return nil, fmt.Errorf("attachment %q is over %d MB", f.Name, maxUploadBytes>>20)
		}
		total += len(raw)
		if total > maxMessageBytes {
			return nil, fmt.Errorf("the attachments are over %d MB together", maxMessageBytes>>20)
		}
		raws[i] = raw
	}
	dir := t.uploadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = freeUploadName(dir, uploadName(f.Name))
		if err := os.WriteFile(filepath.Join(dir, names[i]), raws[i], 0o644); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// Attach stores files no message carries — what a plan's description needs,
// being written (and saved) long before it is handed to the agent — and
// answers with the paths the agent will read them at.
func (t *Task) Attach(files []ChatFile) ([]string, error) {
	names, err := t.storeUploads(files)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = uploadsMount + "/" + name
	}
	return paths, nil
}

// Preview answers with one of the task's attachments as a data URL. A
// description's files are stored here the moment they are picked, so the
// dashboard has no copy of its own to show a thumbnail of and asks for this
// one. Nothing at all where there is no thumbnail to be had — a name that is
// no attachment of this task (a description may say /uploads/whatever without
// one) or a file no browser would draw — which the dashboard shows as the
// file's name rather than as a failure.
func (t *Task) Preview(name string) string {
	file := filepath.Join(t.uploadsDir(), uploadName(name))
	ctype := mime.TypeByExtension(filepath.Ext(file))
	if !strings.HasPrefix(ctype, "image/") {
		return ""
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

// saveUploads stores a message's attachments and returns its text with the
// paths corrected: a file stored under another name than the one the dashboard
// picked (and wrote into the message) is followed there.
func (t *Task) saveUploads(text string, files []ChatFile) (string, error) {
	names, err := t.storeUploads(files)
	if err != nil {
		return text, err
	}
	// Renames are collected and applied in one pass at the end: replacing them
	// one by one could rewrite a path another attachment just got given.
	var renames []string
	for i, f := range files {
		asked := uploadName(f.Name)
		if names[i] != asked {
			renames = append(renames, uploadsMount+"/"+asked, uploadsMount+"/"+names[i])
		}
		// A client that wrote the name unfolded into its message (this
		// dashboard folds it the same way beforehand) is followed too.
		if raw := strings.TrimSpace(f.Name); raw != "" && raw != asked {
			renames = append(renames, uploadsMount+"/"+raw, uploadsMount+"/"+names[i])
		}
	}
	if len(renames) > 0 {
		text = strings.NewReplacer(renames...).Replace(text)
	}
	return text, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// uploadName makes a name safe to write and plain to read: the base name with
// anything exotic folded into a dash, so that what arrives is a file in the
// uploads directory and nothing else, whatever was sent.
func uploadName(name string) string {
	name = unsafeName.ReplaceAllString(filepath.Base(strings.TrimSpace(name)), "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "file"
	}
	if len(name) > 80 {
		stem, ext := splitExt(name)
		name = stem[:min(len(stem), 80-len(ext))] + ext
	}
	return name
}

var trailingNumber = regexp.MustCompile(`-(\d+)$`)

// freeUploadName is the name itself while it is free, and otherwise it with a
// number worked in: image.png becomes image-2.png, and image-2.png image-3.png
// rather than image-2-2.png — the same screenshot pasted again keeps counting.
func freeUploadName(dir, name string) string {
	if !exists(filepath.Join(dir, name)) {
		return name
	}
	stem, ext := splitExt(name)
	n := 2
	if m := trailingNumber.FindStringSubmatch(stem); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil && v >= 1 {
			stem, n = strings.TrimSuffix(stem, m[0]), v+1
		}
	}
	for ; ; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		if !exists(filepath.Join(dir, candidate)) {
			return candidate
		}
	}
}

// splitExt cuts a name into its stem and extension; a name that is all
// extension (".gitignore") is all stem.
func splitExt(name string) (string, string) {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i], name[i:]
	}
	return name, ""
}
