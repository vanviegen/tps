package daemon

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
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
	st := &guestTool{root: dir}
	if _, err := st.put("openrouter", 3); err != nil {
		t.Fatal(err)
	}

	task.takeORRequest(3)
	if task.info.ORAsk != 3 || task.orBusy {
		t.Fatalf("the request should wait for the user: asking %g", task.info.ORAsk)
	}
	if err := task.GrantOpenRouter("3.5"); err == nil || task.info.ORAsk != 3 {
		t.Error("a budget leaving $2.50 should not grant $3, and leave the request standing")
	}
	if err := task.RejectOpenRouter(); err != nil {
		t.Fatal(err)
	}
	if code := st.openRouter([]string{"3"}); code == 0 || len(readRequests(dir, "openrouter", true)) > 0 {
		t.Errorf("a rejected request should fail, and be gone once read: %d", code)
	}
}

// The tool prints the key the daemon answered with.
func TestOpenRouterToolReadsKey(t *testing.T) {
	dir := t.TempDir()
	id, err := (&guestTool{root: dir}).put("openrouter", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := answerRequest(dir, id, orAnswer{Key: "sk-or-test", Limit: 3}); err != nil {
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

type orStub func(*http.Request) *http.Response

func (f orStub) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

// The first grant makes the task's key, and a later one raises its limit
// rather than making another.
func TestOpenRouterRaisesTheKey(t *testing.T) {
	task, _ := testTask(t)
	if err := os.MkdirAll(task.servicesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []string
	transport := openRouterClient.Transport
	openRouterClient.Transport = orStub(func(r *http.Request) *http.Response {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"key":"sk-or-1","data":{"hash":"h1"}}`))}
	})
	defer func() { openRouterClient.Transport = transport }()

	task.grantOpenRouter(2)
	task.grantOpenRouter(1.5)
	if k := task.info.ORKey; k == nil || k.Key != "sk-or-1" || k.Limit != 3.5 || task.info.ORGranted != 3.5 {
		t.Fatalf("got %+v, granted %g", task.info.ORKey, task.info.ORGranted)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "POST /api/v1/keys ") || calls[1] != `PATCH /api/v1/keys/h1 {"limit":3.5}` {
		t.Errorf("calls: %q", calls)
	}
}

// A request the key has enough left for answers with the key as it is, and one
// it has too little left for raises its limit by what it lacks.
func TestOpenRouterRaisesWhatTheKeyLacks(t *testing.T) {
	task, _ := testTask(t)
	dir := task.servicesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	budget := 5.0
	task.info.ORBudget, task.info.ORGranted = &budget, 2
	task.info.ORKey = &orKey{Key: "sk-or-1", Hash: "h1", Limit: 2}
	var calls []string
	transport := openRouterClient.Transport
	openRouterClient.Transport = orStub(func(r *http.Request) *http.Response {
		body := ""
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			body = string(data)
		}
		calls = append(calls, r.Method+" "+r.URL.Path+" "+body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"hash":"h1","limit_remaining":1.5}}`))}
	})
	defer func() { openRouterClient.Transport = transport }()
	st := &guestTool{root: dir}

	if _, err := st.put("openrouter", 1); err != nil {
		t.Fatal(err)
	}
	task.takeORRequest(1)
	if code := st.openRouter([]string{"1"}); code != 0 || task.info.ORGranted != 2 || len(calls) != 1 {
		t.Fatalf("a key with enough left should be handed out as it is: %d, granted %g, calls %q", code, task.info.ORGranted, calls)
	}

	if _, err := st.put("openrouter", 2); err != nil {
		t.Fatal(err)
	}
	task.orBusy = true
	task.takeORRequest(2)
	for busy := true; busy; time.Sleep(10 * time.Millisecond) {
		task.lock()
		busy = task.orBusy
		task.unlock()
	}
	if k := task.info.ORKey; k.Limit != 2.5 || task.info.ORGranted != 2.5 || calls[2] != `PATCH /api/v1/keys/h1 {"limit":2.5}` {
		t.Errorf("got %+v, granted %g, calls %q", k, task.info.ORGranted, calls)
	}
}
