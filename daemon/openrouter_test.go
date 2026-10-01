package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A request beyond the task's OpenRouter budget waits for the user, the budget
// they answer with has to make room for all of it, and turning it down is the
// answer the tool reads.
func TestOpenRouterRequestBeyondBudget(t *testing.T) {
	task, _ := testTask(t)
	dir := task.servicesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	task.p.m.orKey = "mgmt"
	budget := 2.0
	task.info.ORBudget, task.info.ORGranted = &budget, 1
	if err := os.WriteFile(filepath.Join(dir, orRequestFile), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	task.checkORRequestL()
	if task.info.ORAsk != 3 || task.orBusy {
		t.Fatalf("the request should wait for the user: asking %g", task.info.ORAsk)
	}
	if err := task.GrantOpenRouter("3.5"); err == nil || task.info.ORAsk != 3 {
		t.Error("a budget leaving $2.50 should not grant $3, and leave the request standing")
	}
	if err := task.RejectOpenRouter(); err != nil {
		t.Fatal(err)
	}
	st := &guestTool{root: dir}
	if code := st.openRouter([]string{"3"}); code == 0 || exists(filepath.Join(dir, orRequestFile)) {
		t.Errorf("a rejected request should fail, and be gone once read: %d", code)
	}
}

// The tool prints the key the daemon answered with.
func TestOpenRouterToolReadsKey(t *testing.T) {
	dir := t.TempDir()
	writeORAnswer(dir, orAnswer{Key: "sk-or-test", Limit: 3})
	if err := os.WriteFile(filepath.Join(dir, orRequestFile), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	code := (&guestTool{root: dir}).openRouter([]string{"3"})
	os.Stdout = stdout
	w.Close()
	out := make([]byte, 100)
	n, _ := r.Read(out)
	if code != 0 || strings.TrimSpace(string(out[:n])) != "sk-or-test" {
		t.Errorf("got %d %q", code, out[:n])
	}
}
