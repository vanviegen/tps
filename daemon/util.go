package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

type RunResult struct {
	Code     int
	Out, Err string
}

type RunOpts struct {
	Dir     string
	Input   *string
	NoCheck bool // don't fail on a non-zero exit
}

// runCmd runs a command to completion, capturing stdout and stderr.
func runCmd(argv []string, o RunOpts) (RunResult, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = o.Dir
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
