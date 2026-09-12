package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type RunResult struct {
	Code     int
	Out, Err string
}

type RunOpts struct {
	Dir     string
	Input   *string
	Env     []string      // added to the environment
	NoCheck bool          // don't fail on a non-zero exit
	Timeout time.Duration // kill the command after this long (0: no limit)
}

// runCmd runs a command to completion, capturing stdout and stderr.
func runCmd(argv []string, o RunOpts) (RunResult, error) {
	ctx := context.Background()
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = o.Dir
	if len(o.Env) > 0 {
		cmd.Env = append(os.Environ(), o.Env...)
	}
	if o.Input != nil {
		cmd.Stdin = strings.NewReader(*o.Input)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := RunResult{Out: out.String(), Err: errb.String()}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return RunResult{Code: -1}, fmt.Errorf("%s: %w", argv[0], err)
		}
		r.Code = ee.ExitCode()
	}
	if r.Code != 0 && !o.NoCheck {
		msg := strings.TrimSpace(r.Err)
		if msg == "" {
			msg = strings.TrimSpace(r.Out)
		}
		if len(msg) > 4000 {
			msg = msg[:4000]
		}
		return r, fmt.Errorf("`%s` failed (%d): %s", strings.Join(argv, " "), r.Code, msg)
	}
	return r, nil
}

// exitCode of a finished command: its status, or -1 when it did not run to an exit.
func exitCode(err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		return -1
	}
}

// git runs git in dir, returning trimmed stdout.
func git(dir string, args ...string) (string, error) {
	r, err := runCmd(append([]string{"git", "-C", dir}, args...), RunOpts{})
	return strings.TrimSuffix(r.Out, "\n"), err
}

// gitOK reports whether a git command succeeds.
func gitOK(dir string, args ...string) bool {
	r, err := runCmd(append([]string{"git", "-C", dir}, args...), RunOpts{NoCheck: true})
	return err == nil && r.Code == 0
}

func sha256hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(text string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(text), "-"), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		return "x"
	}
	return s
}

// readFile returns the file's content, or "" when it cannot be read.
func readFile(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

// rmTree removes a directory tree the daemon does not own every file of. A
// container that writes as root — its own sudo, or a sibling it started
// without --user — leaves files belonging to a subuid of ours, and unlinking
// those is not ours to do: the directory holding them is the subuid's too. The
// tree is therefore moved aside first, so that the path is free whether or not
// the removal gets anywhere, and then removed as root in a container (see
// rootRm). An error says what is left where; the path asked about is gone
// either way.
func rmTree(dir string) error {
	if !exists(dir) {
		return nil
	}
	trash := fmt.Sprintf("%s.trash-%d", dir, time.Now().UnixNano())
	if err := os.Rename(dir, trash); err != nil {
		return err
	}
	if err := os.RemoveAll(trash); err == nil {
		return nil
	}
	if err := rootRm(trash); err != nil {
		return fmt.Errorf("%s holds files a container wrote as root, and is left behind: %w", trash, err)
	}
	return nil
}

// rootRm removes a path as root in the user namespace the task containers
// share — the root that wrote the files we cannot unlink ourselves. A
// container run as uid 0 with their mapping is that same root, and every uid
// it can meet is mapped there, so a plain rm reaches all of it.
//
// A container, rather than `podman unshare`: this is the one privilege TPS
// already has wherever it runs, and it asks nothing of the runtime beyond
// running one. It therefore works as well when TPS runs inside a TPS task,
// where its podman is the docker CLI on the socket its own container is
// served: siblings there are in that namespace to begin with.
func rootRm(path string) error {
	image, err := anyImage()
	if err != nil {
		return err
	}
	// The parent is mounted, not the path itself: a mount point is not
	// something the container could unlink.
	_, err = runCmd([]string{"podman", "run", "--rm",
		"--userns=keep-id:uid=1000,gid=1000", "--user", "0:0",
		"--security-opt", "label=disable", // as the task's own container, so the mount needs no relabeling
		"--entrypoint", "rm", // whatever the image would have run instead
		"-v", filepath.Dir(path) + ":/trash",
		image, "-rf", "--", "/trash/" + filepath.Base(path)}, RunOpts{Timeout: 5 * time.Minute})
	return err
}

// anyImage is any image TPS has built, as a vehicle for rootRm: all it has to
// provide is rm, and one exists whenever files it is needed for do — they were
// written by a container, which had an image to be.
func anyImage() (string, error) {
	r, err := runCmd([]string{"podman", "images", "--format", "{{.Repository}}:{{.Tag}}", imageRepo}, RunOpts{})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(r.Out, "\n") {
		if tag := strings.TrimSpace(line); tag != "" && !strings.HasSuffix(tag, ":<none>") {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no %s image to run rm in", imageRepo)
}
