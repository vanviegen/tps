package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Version is the release tag this binary was built from, set by the release
// workflow; a build from source has none and is never replaced.
var Version string

var updating sync.Mutex

// autoUpdate replaces our binary by the latest GitHub release when that is
// another one than ours, and restarts into it with args. The daemons follow,
// as they do any new build: once nothing runs on them.
func autoUpdate(args []string) {
	if !updating.TryLock() {
		return
	}
	defer updating.Unlock()
	if Version == "" {
		log.Printf("auto-update: a build from source is not updated")
		return
	}
	exe, err := update()
	if err != nil {
		log.Printf("auto-update: %v", err)
		return
	}
	if exe == "" {
		return
	}
	log.Printf("auto-update: restarting into the new release")
	log.Fatal(syscall.Exec(exe, args, os.Environ()))
}

// update installs the latest release over our executable, and returns its
// path; an empty one when we run the latest already.
func update() (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get("https://api.github.com/repos/vanviegen/tps/releases/latest")
	if err != nil {
		return "", err
	}
	var latest struct {
		Tag string `json:"tag_name"`
	}
	err = json.NewDecoder(resp.Body).Decode(&latest)
	resp.Body.Close()
	if err != nil || latest.Tag == "" {
		return "", fmt.Errorf("no latest release (%s, %v)", resp.Status, err)
	}
	if latest.Tag == Version {
		return "", nil
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	log.Printf("auto-update: %s replaces %s", latest.Tag, Version)
	if resp, err = client.Get("https://github.com/vanviegen/tps/releases/download/" + latest.Tag + "/tps-linux-" + arch); err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", latest.Tag, resp.Status)
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", err
	}
	// Written next to it and renamed over it, so that it is never half there.
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".tps-update-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), exe)
	}
	return exe, err
}
