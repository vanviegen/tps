package daemon

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// The toolbox: code-server, the agent CLIs and a tiny init, downloaded once
// per host and mounted read-only into every task container at /tps. Images
// stay free of anything TPS-specific, and updating the tools (bump the
// versions here, or a provider's in its own file) never needs an image
// rebuild. Layout: bin/{code-server,claude,pi,tini}, the releases that are
// more than a binary under directories of their own, and two things of ours:
// the default Containerfile.dev, and this binary as bin/tps-guest-tool
// (see guesttool.go), refreshed whenever the daemon is a new build.
const (
	codeServerVersion = "4.135.0"
	tiniVersion       = "0.19.0"
)

// toolboxKey names the exact set of tools a toolbox holds, so that a container
// says which one it runs on and a bumped version is a new directory.
func toolboxKey() string {
	key := "code-server-" + codeServerVersion + "_tini-" + tiniVersion
	for _, p := range providers {
		key += "_" + p.Name() + "-" + p.Version()
	}
	return key
}

func toolboxRoot() string    { return filepath.Join(home(), ".local", "share", "tps", "toolbox") }
func toolboxDir() string     { return filepath.Join(toolboxRoot(), toolboxKey()) }
func toolboxInstalled() bool { return exists(toolboxDir()) }

// claudeBin is the claude binary in this host's toolbox.
func claudeBin() string { return filepath.Join(toolboxDir(), "bin", "claude") }

// ClaudeBin is that binary for the dashboard, which signs hosts in with it
// (see ui/login.go): the toolbox is fetched for it, as for a task.
func ClaudeBin() (string, error) {
	if _, err := ensureToolbox(); err != nil {
		return "", err
	}
	return claudeBin(), nil
}

var toolboxMu sync.Mutex

// ensureToolbox downloads the toolbox if this host lacks it, and removes
// toolboxes of other versions once no container runs on them anymore.
func ensureToolbox() (string, error) {
	toolboxMu.Lock()
	defer toolboxMu.Unlock()
	dir := toolboxDir()
	if !exists(dir) {
		// Into a directory of this download's own: the dashboard and the daemon
		// share the toolbox and may both be fetching it, and whichever finishes
		// second finds it there.
		_ = os.MkdirAll(toolboxRoot(), 0o755)
		tmp, err := os.MkdirTemp(toolboxRoot(), ".download-")
		if err != nil {
			return "", err
		}
		if err := downloadToolbox(tmp); err != nil {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("downloading the TPS toolbox: %w", err)
		}
		if err := os.Rename(tmp, dir); err != nil && !exists(dir) {
			return "", err
		}
		os.RemoveAll(tmp)
	}
	if cf := filepath.Join(dir, containerfile); readFile(cf) != defaultContainerfile {
		if err := os.WriteFile(cf, []byte(defaultContainerfile), 0o644); err != nil {
			return "", err
		}
	}
	if err := installGuestTool(filepath.Join(dir, "bin")); err != nil {
		return "", err
	}
	// Containers are labeled with the key of the toolbox they run on.
	if ps, err := runCmd([]string{"podman", "ps", "--format", `{{index .Labels "tps.config"}}`}, RunOpts{}); err == nil {
		entries, _ := os.ReadDir(toolboxRoot())
		for _, e := range entries {
			if e.Name() != toolboxKey() && !strings.HasPrefix(e.Name(), ".") && !strings.Contains(ps.Out, `"`+e.Name()+`"`) { // dot: a download under way
				os.RemoveAll(filepath.Join(toolboxRoot(), e.Name()))
			}
		}
	}
	return dir, nil
}

func downloadToolbox(dir string) error {
	arch := runtime.GOARCH // containers share the host's kernel and architecture
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("code-server-%s-linux-%s", codeServerVersion, arch)
	if err := fetch("https://github.com/coder/code-server/releases/download/v"+codeServerVersion+"/"+name+".tar.gz", func(r io.Reader) error {
		return untar(r, filepath.Join(dir, "code-server"))
	}); err != nil {
		return err
	}
	if err := fetch("https://github.com/krallin/tini/releases/download/v"+tiniVersion+"/tini-static-"+arch, func(r io.Reader) error {
		return writeFile(filepath.Join(bin, "tini"), r, 0o755)
	}); err != nil {
		return err
	}
	for _, p := range providers {
		if err := p.Install(dir); err != nil {
			return fmt.Errorf("installing %s: %w", p.Name(), err)
		}
	}
	return os.Symlink("../code-server/bin/code-server", filepath.Join(bin, "code-server"))
}

func fetch(url string, read func(io.Reader) error) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := read(resp.Body); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return nil
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// untar extracts a .tar.gz stream into dir, dropping the archive's top-level directory.
func untar(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		_, name, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		if !ok || name == "" || !filepath.IsLocal(name) {
			continue
		}
		path := filepath.Join(dir, name)
		mode := os.FileMode(h.Mode) & 0o777
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, mode|0o700)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = writeFile(path, tr, mode)
			}
		case tar.TypeSymlink:
			err = os.Symlink(h.Linkname, path)
		}
		if err != nil {
			return err
		}
	}
}

// refreshGuestTool puts this build's guest tool in every toolbox this host
// keeps, and is what the daemon does about them at startup. The tool is the
// daemon's own binary rather than one of the downloaded versions a toolbox is
// keyed by, and /tps in a container is the toolbox it was started on, which an
// upgraded daemon does not rebuild: writing to all of them is how a container
// that outlived the upgrade gets the tool its agent is told to run. A host
// with no toolbox yet is left to download one when a task needs it (see
// ensureToolbox).
func refreshGuestTool() {
	toolboxMu.Lock()
	defer toolboxMu.Unlock()
	entries, _ := os.ReadDir(toolboxRoot())
	for _, e := range entries {
		bin := filepath.Join(toolboxRoot(), e.Name(), "bin")
		if !e.IsDir() || !exists(bin) {
			continue
		}
		if err := installGuestTool(bin); err != nil {
			logf("refreshing %s in %s: %v", guestToolName, e.Name(), err)
		}
	}
}

// installGuestTool puts this binary in the toolbox as tps-guest-tool
// (see guesttool.go), unless the same build is there already. It goes in
// under a temporary name and is renamed over the old one: a wrapper still
// running the old copy keeps its inode, and the file of a running program
// cannot be written to in place anyway.
func installGuestTool(bin string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	tool := filepath.Join(bin, guestToolName)
	if readFile(tool+".sha256") == sum && exists(tool) {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := writeFile(tool+".tmp", f, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tool+".tmp", tool); err != nil {
		return err
	}
	return os.WriteFile(tool+".sha256", []byte(sum), 0o644)
}
