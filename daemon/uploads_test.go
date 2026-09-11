package daemon

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vanviegen/agent-manager/hub"
)

func TestUploadName(t *testing.T) {
	for in, want := range map[string]string{
		"image.png":                      "image.png",
		"  Screen shot 12:04.png  ":      "Screen-shot-12-04.png",
		"../../etc/passwd":               "passwd",
		"/tmp/x/../y.png":                "y.png",
		".hidden":                        "hidden",
		"":                               "file",
		strings.Repeat("a", 99) + ".png": strings.Repeat("a", 76) + ".png",
	} {
		if got := uploadName(in); got != want {
			t.Errorf("uploadName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFreeUploadName(t *testing.T) {
	dir := t.TempDir()
	take := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := freeUploadName(dir, "image.png"); got != "image.png" {
		t.Errorf("free name taken: %q", got)
	}
	take("image.png")
	take("image-2.png")
	// The number counts on rather than piling up: not image-2-2.png.
	if got := freeUploadName(dir, "image.png"); got != "image-3.png" {
		t.Errorf("after image-2.png: %q", got)
	}
	if got := freeUploadName(dir, "image-2.png"); got != "image-3.png" {
		t.Errorf("numbered name: %q", got)
	}
	take("notes")
	if got := freeUploadName(dir, "notes"); got != "notes-2" {
		t.Errorf("no extension: %q", got)
	}
}

// Attachments land in the task's uploads dir, and the message follows them
// there when the name it asked for was taken.
func TestSaveUploads(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{projects: map[string]*Project{}, dataDir: filepath.Join(dir, "data"), hub: hub.New(nil)}
	p := &Project{m: m, pid: "project", info: &ProjectInfo{Dir: dir}, tasks: map[string]*Task{}, defaultBranch: "main"}
	m.projects[p.pid] = p
	task := newTask(p, "1", &TaskInfo{Phase: PhaseAgent})
	p.tasks[task.tid] = task

	file := func(name, body string) ChatFile {
		return ChatFile{Name: name, Data: base64.StdEncoding.EncodeToString([]byte(body))}
	}
	text, err := task.saveUploads("look at /uploads/image.png", []ChatFile{file("image.png", "first")})
	if err != nil {
		t.Fatal(err)
	}
	if text != "look at /uploads/image.png" {
		t.Errorf("a free name needs no correction: %q", text)
	}
	if body, _ := os.ReadFile(filepath.Join(task.uploadsDir(), "image.png")); string(body) != "first" {
		t.Errorf("not written: %q", body)
	}

	// Two more of the same name, one of them under the name the other is about
	// to be given: both move up, and neither takes the other's path with it.
	text, err = task.saveUploads("/uploads/image.png and /uploads/image-2.png", []ChatFile{
		file("image.png", "second"), file("image-2.png", "third"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "/uploads/image-2.png and /uploads/image-3.png" {
		t.Errorf("paths not corrected: %q", text)
	}
	for name, want := range map[string]string{"image.png": "first", "image-2.png": "second", "image-3.png": "third"} {
		if body, _ := os.ReadFile(filepath.Join(task.uploadsDir(), name)); string(body) != want {
			t.Errorf("%s holds %q, want %q", name, body, want)
		}
	}

	// A name that had to be folded, referred to in the message as it was sent.
	text, err = task.saveUploads("see /uploads/Screen shot.png", []ChatFile{file("Screen shot.png", "fourth")})
	if err != nil {
		t.Fatal(err)
	}
	if text != "see /uploads/Screen-shot.png" {
		t.Errorf("folded name not followed: %q", text)
	}

	// Too much to take: the message keeps its text and nothing is written.
	big := ChatFile{Name: "big.png", Data: base64.StdEncoding.EncodeToString(make([]byte, maxUploadBytes+1))}
	if _, err := task.saveUploads("/uploads/big.png", []ChatFile{big}); err == nil {
		t.Error("an oversized attachment was accepted")
	}
	if exists(filepath.Join(task.uploadsDir(), "big.png")) {
		t.Error("an oversized attachment was written")
	}
}
