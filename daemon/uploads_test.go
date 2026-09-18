package daemon

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vanviegen/tps/hub"
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

// A task to attach to, with nothing else around it.
func uploadsTask(t *testing.T) *Task {
	dir := t.TempDir()
	m := &Manager{projects: map[string]*Project{}, dataDir: filepath.Join(dir, "data"), hub: hub.New(nil)}
	p := &Project{m: m, pid: "project", info: &ProjectInfo{Dir: dir}, tasks: map[string]*Task{}, defaultBranch: "main"}
	m.projects[p.pid] = p
	task := newTask(p, "1", &TaskInfo{Phase: PhaseAgent})
	p.tasks[task.tid] = task
	return task
}

func file(name, body string) ChatFile {
	return ChatFile{Name: name, Data: base64.StdEncoding.EncodeToString([]byte(body))}
}

// Attachments land in the task's uploads dir, and the message follows them
// there when the name it asked for was taken.
func TestSaveUploads(t *testing.T) {
	task := uploadsTask(t)
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

// What a description attaches has no text to correct: the paths the files
// ended up at are the answer.
func TestAttach(t *testing.T) {
	task := uploadsTask(t)
	paths, err := task.Attach([]ChatFile{file("Screen shot.png", "first"), file("Screen shot.png", "second")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/uploads/Screen-shot.png", "/uploads/Screen-shot-2.png"}
	if !slices.Equal(paths, want) {
		t.Errorf("attached at %v, want %v", paths, want)
	}
	for i, path := range paths {
		if body, _ := os.ReadFile(filepath.Join(task.uploadsDir(), filepath.Base(path))); string(body) != []string{"first", "second"}[i] {
			t.Errorf("%s holds %q", path, body)
		}
	}
}

// A thumbnail is the stored file as a data URL, and nothing outside the
// uploads directory can be asked for.
func TestPreview(t *testing.T) {
	task := uploadsTask(t)
	if _, err := task.Attach([]ChatFile{file("shot.png", "bytes")}); err != nil {
		t.Fatal(err)
	}
	if want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("bytes")); task.Preview("shot.png") != want {
		t.Errorf("preview is %q, want %q", task.Preview("shot.png"), want)
	}
	if err := os.WriteFile(filepath.Join(task.dir(), "secret.png"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if url := task.Preview("../secret.png"); url != "" {
		t.Errorf("a file outside the uploads directory was handed over: %q", url)
	}
}
