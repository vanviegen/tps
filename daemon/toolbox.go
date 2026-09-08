package daemon

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// The toolbox: code-server, claude and a tiny init, downloaded once per host
// and mounted read-only into every task container at /tps. Images stay free
// of anything TPS-specific, and updating the tools (bump the versions here)
// never needs an image rebuild. Layout: bin/{code-server,claude,tini} and the
// code-server release under code-server/.
const (
	codeServerVersion = "4.135.0"
	claudeVersion     = "2.1.263"
	tiniVersion       = "0.19.0"
)

var toolboxKey = "code-server-" + codeServerVersion + "_claude-" + claudeVersion + "_tini-" + tiniVersion

func toolboxRoot() string    { return filepath.Join(home(), ".local", "share", "tps", "toolbox") }
func toolboxDir() string     { return filepath.Join(toolboxRoot(), toolboxKey) }
func toolboxInstalled() bool { return exists(toolboxDir()) }

var toolboxMu sync.Mutex

// ensureToolbox downloads the toolbox if this host lacks it, and removes
// toolboxes of other versions once no container runs on them anymore.
func ensureToolbox() (string, error) {
	toolboxMu.Lock()
	defer toolboxMu.Unlock()
	dir := toolboxDir()
	if !exists(dir) {
		tmp := dir + ".tmp"
		os.RemoveAll(tmp)
		if err := downloadToolbox(tmp); err != nil {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("downloading the TPS toolbox: %w", err)
		}
		if err := os.Rename(tmp, dir); err != nil {
			return "", err
		}
	}
	// Containers are labeled with the key of the toolbox they run on.
	if ps, err := runCmd([]string{"podman", "ps", "--format", `{{index .Labels "tps.config"}}`}, RunOpts{}); err == nil {
		entries, _ := os.ReadDir(toolboxRoot())
		for _, e := range entries {
			if e.Name() != toolboxKey && !strings.Contains(ps.Out, `"`+e.Name()+`"`) {
				os.RemoveAll(filepath.Join(toolboxRoot(), e.Name()))
			}
		}
	}
	return dir, nil
}

func downloadToolbox(dir string) error {
	arch := runtime.GOARCH // containers share the host's kernel and architecture
	claudeArch, ok := map[string]string{"amd64": "x64", "arm64": "arm64"}[arch]
	if !ok {
		return fmt.Errorf("no code-server/claude builds for %s", arch)
	}
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
	for _, f := range [][2]string{
		{"https://downloads.claude.ai/claude-code-releases/" + claudeVersion + "/linux-" + claudeArch + "/claude", "claude"},
		{"https://github.com/krallin/tini/releases/download/v" + tiniVersion + "/tini-static-" + arch, "tini"},
	} {
		if err := fetch(f[0], func(r io.Reader) error { return writeFile(filepath.Join(bin, f[1]), r, 0o755) }); err != nil {
			return err
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
