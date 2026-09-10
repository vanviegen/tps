package podnester

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
)

// Bind-mount sources reach the proxy as paths in the top-level container's
// filesystem: that is the only filesystem its clients know. A sibling may be
// given anything that container can see, and nothing else. What it can see
// is its own root filesystem (an overlay podman keeps mounted on the host for
// as long as the container runs) and the bind mounts it was started with, so
// a container path is turned into the host path naming the same file, and
// that is what podman gets.
//
// Symlinks are the catch. Podman resolves the path it is given on the host,
// so a link in the container pointing at /home/user/.ssh would open exactly
// that on the host. The walk below follows links the way the container's
// kernel does — inside the container's view, where an absolute target starts
// over at the container's root — and hands podman a path with no links left
// in it.

// mountTable is the top-level container's view: its root filesystem on the
// host, and its bind mounts.
type mountTable struct {
	Root   string            // host path of the container's root filesystem
	Mounts map[string]string // container path → host path
}

// host is the host path a container path lexically maps to (no link
// following): under the longest bind mount that contains it, or else under
// the root.
func (t *mountTable) host(cpath string) string {
	best := ""
	for c := range t.Mounts {
		if (cpath == c || strings.HasPrefix(cpath, strings.TrimSuffix(c, "/")+"/")) && len(c) > len(best) {
			best = c
		}
	}
	if best == "" {
		return path.Join(t.Root, cpath)
	}
	return path.Join(t.Mounts[best], strings.TrimPrefix(cpath, strings.TrimSuffix(best, "/")))
}

// errNeedRoot says the walk had to look inside the root filesystem, and that
// is not visible from here (rootless podman keeps it in a mount namespace of
// its own): the walk must be redone from inside that namespace.
var errNeedRoot = errors.New("the container's root filesystem is not visible from this mount namespace")

const maxLinks = 255

// resolve turns a container path into the host path it names, following
// symlinks within the container's view. Components that do not exist are
// kept as they are (podman creates missing bind sources); podman then finds
// no links on the way, as every existing component was looked at.
func (t *mountTable) resolve(cpath string) (string, error) {
	if !path.IsAbs(cpath) {
		return "", fmt.Errorf("%s: not an absolute path", cpath)
	}
	rootVisible := false
	if fi, err := os.Lstat(t.Root); err == nil && fi.IsDir() {
		rootVisible = true
	}
	inRoot := func(hpath string) bool {
		for _, h := range t.Mounts {
			if hpath == h || strings.HasPrefix(hpath, strings.TrimSuffix(h, "/")+"/") {
				return false
			}
		}
		return true
	}
	cur := "/"
	queue := split(cpath)
	links := 0
	for len(queue) > 0 {
		comp := queue[0]
		queue = queue[1:]
		next := path.Join(cur, comp) // handles "." and ".." against the virtual cur
		hpath := t.host(next)
		if !rootVisible && inRoot(hpath) {
			return "", errNeedRoot
		}
		fi, err := os.Lstat(hpath)
		if errors.Is(err, os.ErrNotExist) {
			// Nothing further down can be a link. The rest must be plain names.
			rest := path.Join(append([]string{next}, queue...)...)
			if !strings.HasPrefix(rest, "/") || strings.Contains(rest, "/../") || strings.HasSuffix(rest, "/..") {
				return "", fmt.Errorf("%s: invalid path", cpath)
			}
			return t.host(path.Clean(rest)), nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if links++; links > maxLinks {
				return "", fmt.Errorf("%s: too many levels of symbolic links", cpath)
			}
			target, err := os.Readlink(hpath)
			if err != nil {
				return "", err
			}
			if path.IsAbs(target) {
				cur = "/"
			}
			queue = append(split(target), queue...)
			continue
		}
		cur = next
	}
	return t.host(cur), nil
}

func split(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

// resolveVia runs the walk in a helper process when it cannot be done here:
// helper is a command prefix (typically `podman unshare <this binary>
// podnester-resolve`) that reads a resolveJob and prints a resolveResult.
func (t *mountTable) resolveVia(helper []string, cpath string) (string, error) {
	hpath, err := t.resolve(cpath)
	if !errors.Is(err, errNeedRoot) {
		return hpath, err
	}
	if len(helper) == 0 {
		return "", fmt.Errorf("%s: %v, and no resolver helper is configured", cpath, err)
	}
	job, _ := json.Marshal(resolveJob{Table: *t, Path: cpath})
	cmd := exec.Command(helper[0], helper[1:]...)
	cmd.Stdin = bytes.NewReader(job)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("resolving %s: %s: %v: %s", cpath, strings.Join(helper, " "), err, strings.TrimSpace(stderr.String()))
	}
	var res resolveResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		return "", fmt.Errorf("resolving %s: bad helper output: %v", cpath, err)
	}
	if res.Error != "" {
		return "", errors.New(res.Error)
	}
	return res.Host, nil
}

type resolveJob struct {
	Table mountTable
	Path  string
}

type resolveResult struct {
	Host  string
	Error string `json:",omitempty"`
}

// ResolveMain is the helper side of resolveVia: a resolveJob on stdin, a
// resolveResult on stdout. Binaries embedding the proxy run it when invoked
// with the podnester-resolve subcommand (see Subcommand).
func ResolveMain(stdin io.Reader, stdout io.Writer) error {
	var job resolveJob
	if err := json.NewDecoder(stdin).Decode(&job); err != nil {
		return err
	}
	res := resolveResult{}
	if hpath, err := job.Table.resolve(job.Path); err != nil {
		res.Error = err.Error()
	} else {
		res.Host = hpath
	}
	return json.NewEncoder(stdout).Encode(res)
}

// sortedMounts lists a table's container paths, longest first (for messages and tests).
func (t *mountTable) sortedMounts() []string {
	var keys []string
	for k := range t.Mounts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}
