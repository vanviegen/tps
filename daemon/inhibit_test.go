package daemon

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// A helper started this way must die of its own accord once the daemon lets go
// of the pipe, whether it is closed or the daemon is gone entirely.
func TestInhibitHelperFollowsPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cat")
	cmd.Stdin = r
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("helper left early: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	w.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper exited badly: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper outlived its pipe")
	}
}
