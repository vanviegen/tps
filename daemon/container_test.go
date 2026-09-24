package daemon

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Only a failed build is the Containerfile's fault: it is what falls back to
// the default image and sends the agent in to fix the file (see doUp). A
// container that would not start — podman refusing the name, say — is the
// host's doing, and must not be read as a broken Containerfile.
func TestBuildErr(t *testing.T) {
	build := buildErr{errors.New("podman build failed (1): no such base image")}
	if !isBuildErr(build) || !isBuildErr(fmt.Errorf("starting it: %w", build)) {
		t.Error("a build failure is not recognised as one")
	}
	if isBuildErr(nil) || isBuildErr(errors.New(`the container name "tps-x-1" is already in use`)) {
		t.Error("a failure that is not the build's is taken for one")
	}
}

// A terminal is busy while its shell runs something, and idle at its prompt:
// a code-server stand-in whose "pty host" has a shell under it, that shell
// with a child of its own or without one. The marker is split up in the
// stand-in's own command line, so only the pty host carries it.
func TestTermBusyScript(t *testing.T) {
	for _, c := range []struct {
		shell string
		busy  bool
	}{
		{`sleep 30; :`, false},
		{`sh -c "sleep 30; :"; :`, true},
	} {
		code := exec.Command("sh", "-c", `sh -c "$1" "--type=pty""Host" & wait`, "sh", c.shell)
		code.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := code.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		busy := exec.Command("sh", "-c", termBusyScript, "sh", strconv.Itoa(code.Process.Pid)).Run() == nil
		_ = syscall.Kill(-code.Process.Pid, syscall.SIGKILL)
		_ = code.Wait()
		if busy != c.busy {
			t.Errorf("shell %q: busy %v, want %v", c.shell, busy, c.busy)
		}
	}
}
