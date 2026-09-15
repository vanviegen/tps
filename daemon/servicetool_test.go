package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tool re-executes this binary as the wrapper of a service (see
// serviceWrap); the test binary must dispatch on that like main does.
func TestMain(m *testing.M) {
	if handled, code := ServiceToolInvoked(os.Args); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestServiceTool(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".declared"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".declared", "app"), []byte("echo hello declared; sleep 0.3; echo bye\n"), 0o644)
	st := &serviceTool{root: root}
	run := func(args ...string) error { return st.run(args) }
	// waitLog waits for the service to have said something, so that a stop
	// finds it with its traps in place rather than still starting up.
	waitLog := func(name, text string) {
		for i := 0; i < 50 && !strings.Contains(readFile(filepath.Join(root, name, "log")), text); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if err := run("app"); err != nil {
		t.Fatal(err)
	}
	if code := st.await([]string{"app", "5"}); code != 0 {
		t.Errorf("await app: %d", code)
	}
	if log := readFile(filepath.Join(root, "app", "log")); log != "hello declared\nbye\n" {
		t.Errorf("app log: %q", log)
	}
	if cmd := readService(root, "app").Cmd; cmd != "echo hello declared; sleep 0.3; echo bye" {
		t.Errorf("app cmd: %q", cmd)
	}
	if err := run("test", "bash", "-c", `echo "it's a test"; exit 3`); err != nil {
		t.Fatal(err)
	}
	if code := st.await([]string{"test", "5"}); code != 3 {
		t.Errorf("await test: %d", code)
	}
	if cmd := readService(root, "test").Cmd; cmd != "bash -c 'echo \"it'\\''s a test\"; exit 3'" {
		t.Errorf("test cmd: %q", cmd)
	}
	if err := run("web", `trap "echo got TERM; exit 0" TERM; echo ready; while :; do sleep 0.1; done`); err != nil {
		t.Fatal(err)
	}
	waitLog("web", "ready")
	if code := st.await([]string{"web", "0"}); code != 124 {
		t.Errorf("await a running service: %d", code)
	}
	if err := run("web"); err == nil || !strings.Contains(err.Error(), "running already") {
		t.Errorf("double run: %v", err)
	}
	if err := st.stop([]string{"web"}); err != nil {
		t.Fatal(err)
	}
	s := readService(root, "web")
	if s.Status != "stopped" || !strings.Contains(readFile(filepath.Join(root, "web", "log")), "got TERM") {
		t.Errorf("stopped: %+v, log %q", s, readFile(filepath.Join(root, "web", "log")))
	}
	if err := st.restart([]string{"web"}); err != nil {
		t.Fatal(err)
	}
	waitLog("web", "ready")
	if !serviceAlive(filepath.Join(root, "web")) {
		t.Error("restart: not running")
	}
	if err := st.stop([]string{"web"}); err != nil {
		t.Fatal(err)
	}
	// A command without a TERM handler dies of the signal: 128 + 15, as a shell would say.
	if err := run("plain", "bash", "-c", "echo ready; exec sleep 100"); err != nil {
		t.Fatal(err)
	}
	waitLog("plain", "ready")
	if err := st.stop([]string{"plain"}); err != nil {
		t.Fatal(err)
	}
	if s := readService(root, "plain"); s.Status != "stopped" || s.Code == nil || *s.Code != 143 {
		t.Errorf("killed by TERM: %+v", s)
	}
	if err := run("stubborn", `trap "" TERM; echo ready; sleep 100`); err != nil {
		t.Fatal(err)
	}
	waitLog("stubborn", "ready")
	start := time.Now()
	if err := st.stop([]string{"stubborn"}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 4*time.Second || d > 8*time.Second {
		t.Errorf("KILL after the grace period, took %v", d)
	}
	if s := readService(root, "stubborn"); s.Status != "stopped" || *s.Code != 137 {
		t.Errorf("killed: %+v", s)
	}
	// The wrapper keeps its own flag to itself: a service that runs a TPS
	// binary (this project's own does) must not have it take over as wrapper.
	if err := run("env", `echo "wrap=[$TPS_SERVICE_WRAP] color=[$FORCE_COLOR]"`); err != nil {
		t.Fatal(err)
	}
	if code := st.await([]string{"env", "5"}); code != 0 {
		t.Errorf("await env: %d", code)
	}
	// ...and asks for colour, which nothing writing to a log file gives by itself.
	if log := readFile(filepath.Join(root, "env", "log")); log != "wrap=[] color=[1]\n" {
		t.Errorf("the service's environment: %q", log)
	}
	if got := colorEnv([]string{"PATH=/bin", "FORCE_COLOR=0"}); strings.Join(got, " ") != "PATH=/bin FORCE_COLOR=0 CLICOLOR_FORCE=1" {
		t.Errorf("colorEnv overrode what was set: %q", got)
	}
	// A container replaced takes the running processes with it, not the
	// services: an ad hoc one runs again by name alone.
	resetServices(root)
	if s := readService(root, "test"); s.Status != "idle" || s.Cmd == "" {
		t.Errorf("after a reset: %+v", s)
	}
	if err := run("test"); err != nil {
		t.Fatal(err)
	}
	if code := st.await([]string{"test", "5"}); code != 3 {
		t.Errorf("await the ad hoc service, run again: %d", code)
	}
	if err := run("bad name", "x"); err == nil {
		t.Error("bad name accepted")
	}
	if code := st.await([]string{"nothere"}); code != 2 {
		t.Errorf("await unknown: %d", code)
	}
	if err := run("nocmd"); err == nil {
		t.Error("run without a command or declaration")
	}
	if got := lastLines("a\nb\nc\n", 2); len(got) != 2 || got[0] != "b" {
		t.Errorf("lastLines: %q", got)
	}
}
