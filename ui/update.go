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
	// The assets are named as uname -m names the architecture.
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if runtime.GOOS == "darwin" && arch == "aarch64" {
		arch = "arm64"
	}
	log.Printf("auto-update: %s replaces %s", latest.Tag, Version)
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", err
	}
	return exe, download(latest.Tag, "tps-"+runtime.GOOS+"-"+arch, exe)
}

// releaseBinary is this release's build for a Linux host whose architecture
// uname -m names arch, downloaded once; the builds of other releases go.
func releaseBinary(arch string) (string, error) {
	if Version == "" {
		return "", fmt.Errorf("a linux/%s host needs a build of its own: pass one with --daemon-binary", arch)
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".local", "share", "tps", "releases")
	path := filepath.Join(root, Version, "tps-linux-"+arch)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != Version {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, download(Version, "tps-linux-"+arch, path)
}

// download puts a release's asset at path, executable.
func download(tag, asset, path string) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get("https://github.com/vanviegen/tps/releases/download/" + tag + "/" + asset)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s of %s: %s", asset, tag, resp.Status)
	}
	// Written next to it and renamed over it, so that it is never half there.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tps-download-")
	if err != nil {
		return err
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
		err = os.Rename(tmp.Name(), path)
	}
	return err
}
