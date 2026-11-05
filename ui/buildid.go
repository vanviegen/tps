package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
	"time"
)

// BuildID identifies the binary on disk at our executable path: the daemon
// reports its own, and a mismatch with the binary its host gets installed
// (see transport.Build) means a rebuilt binary should replace it.
func BuildID() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	return fileBuild(exe)
}

// fileBuild is the sha256 of a binary. Reading the file each time (cached by
// size and mtime) means a rebuild is noticed by a UI that is still running the
// old code, and never the reverse.
func fileBuild(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "unknown"
	}
	buildCache.Lock()
	defer buildCache.Unlock()
	if b, ok := buildCache.files[path]; ok && st.Size() == b.size && st.ModTime().Equal(b.mtime) {
		return b.id
	}
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unknown"
	}
	id := hex.EncodeToString(h.Sum(nil))
	buildCache.files[path] = build{st.Size(), st.ModTime(), id}
	return id
}

type build struct {
	size  int64
	mtime time.Time
	id    string
}

var buildCache = struct {
	sync.Mutex
	files map[string]build
}{files: map[string]build{}}
