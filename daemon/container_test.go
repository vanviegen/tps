package daemon

import (
	"errors"
	"fmt"
	"testing"
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
