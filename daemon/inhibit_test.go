package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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

func TestKillStaleInhibitors(t *testing.T) {
	sh, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Skip("no /bin/sh to stand in for systemd-inhibit")
	}
	fake := t.TempDir() + "/systemd-inhibit"
	if err := os.WriteFile(fake, sh, 0o755); err != nil {
		t.Fatal(err)
	}
	const who = "TPS-test"
	// "; :" keeps the shell around as a parent, like systemd-inhibit is.
	cmd := exec.Command(fake, "-c", "sleep 60; :", "--who="+who)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var child int
	for range 50 {
		time.Sleep(20 * time.Millisecond)
		for _, dir := range procDirs() {
			if ppid, ok := procParent(dir); ok && ppid == cmd.Process.Pid {
				child, _ = strconv.Atoi(dir[len("/proc/"):])
			}
		}
		if child > 0 {
			break
		}
	}
	if child == 0 {
		t.Fatal("stand-in never forked a child")
	}
	if !isOurInhibitor("/proc/"+strconv.Itoa(cmd.Process.Pid), os.Getuid(), who) {
		t.Fatal("stand-in not recognised as ours")
	}
	killStaleInhibitors(who)
	if err := cmd.Wait(); err == nil {
		t.Fatal("stand-in survived")
	}
	for range 100 { // reparenting and reaping take a moment
		if !alive(child) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child %d survived", child)
}

// alive reports whether a pid is still a running process; a killed child hangs
// around as a zombie until whoever inherited it gets round to reaping.
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := strings.LastIndex(string(data), ")")
	return i >= 0 && !strings.HasPrefix(strings.TrimSpace(string(data)[i+1:]), "Z")
}
