package daemon

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Attachments: files a user adds to a chat message — a screenshot pasted into
// the dashboard, most often. They are kept per task, in a directory mounted
// read-only into its container, and the message refers to them by the path the
// agent reads them at, so nothing but text ever reaches claude.

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

// saveUploads stores a message's attachments and returns its text with the
// paths corrected: the name a dashboard picked may be taken here already (by
// an earlier message, or by another dashboard), and the file is then stored
// under the next free one, which the text must follow. Nothing is written
// unless everything can be: a message arrives with all of its files or none.
func (t *Task) saveUploads(text string, files []ChatFile) (string, error) {
	if len(files) == 0 {
		return text, nil
	}
	raws := make([][]byte, len(files))
	total := 0
	for i, f := range files {
		raw, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return text, fmt.Errorf("attachment %q could not be read", f.Name)
		}
		if len(raw) > maxUploadBytes {
			return text, fmt.Errorf("attachment %q is over %d MB", f.Name, maxUploadBytes>>20)
		}
		total += len(raw)
		if total > maxMessageBytes {
			return text, fmt.Errorf("the attachments are over %d MB together", maxMessageBytes>>20)
		}
		raws[i] = raw
	}
	dir := t.uploadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return text, err
	}
	// Renames are collected and applied in one pass at the end: replacing them
	// one by one could rewrite a path another attachment just got given.
	var renames []string
	for i, f := range files {
		asked := uploadName(f.Name)
		name := freeUploadName(dir, asked)
		if err := os.WriteFile(filepath.Join(dir, name), raws[i], 0o644); err != nil {
			return text, err
		}
		if name != asked {
			renames = append(renames, uploadsMount+"/"+asked, uploadsMount+"/"+name)
		}
		// A client that wrote the name unfolded into its message (this
		// dashboard folds it the same way beforehand) is followed too.
		if raw := strings.TrimSpace(f.Name); raw != "" && raw != asked {
			renames = append(renames, uploadsMount+"/"+raw, uploadsMount+"/"+name)
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
