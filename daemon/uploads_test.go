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

// Attaching answers with the paths the files ended up at.
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
