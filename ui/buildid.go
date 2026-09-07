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
// reports its own, and a mismatch means a rebuilt binary should replace it.
// Reading the file each time (cached by size and mtime) means a rebuild is
// noticed by a UI that is still running the old code, and never the reverse.
var buildCache struct {
	sync.Mutex
	size  int64
	mtime time.Time
	id    string
}

func BuildID() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	st, err := os.Stat(exe)
	if err != nil {
		return "unknown"
	}
	buildCache.Lock()
	defer buildCache.Unlock()
	if buildCache.id != "" && st.Size() == buildCache.size && st.ModTime().Equal(buildCache.mtime) {
		return buildCache.id
	}
	f, err := os.Open(exe)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unknown"
	}
	buildCache.size, buildCache.mtime, buildCache.id = st.Size(), st.ModTime(), hex.EncodeToString(h.Sum(nil))
	return buildCache.id
}
