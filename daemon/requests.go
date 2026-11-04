package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Requests: how the tool asks the daemon for something and waits for the
// answer — a question for the user (see ask.go), OpenRouter spending (see
// openrouter.go), or something about the host's projects (see board.go). The
// container reaches the daemon through nothing but the services dir, so a
// request is a file there, requestsDir/<id>.json, holding its kind and body,
// and its answer is <id>.answer beside it, written by the daemon on its
// services tick or whenever the answer comes in after. The tool takes both away
// once it has the answer. Being files, they outlive a daemon restart, which
// the containers do too; a container that goes takes its requests with it (see
// resetServices).

const requestsDir = ".requests"

// guestRequest is one request as the daemon reads it.
type guestRequest struct {
	ID   string          `json:"-"` // the file's name
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body"`
}

// --- the tool's side ---

// put leaves a request of the kind for the daemon, and says what it is called.
func (st *guestTool) put(kind string, body any) (string, error) {
	dir := filepath.Join(st.root, requestsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	raw, _ := json.Marshal(body)
	data, _ := json.Marshal(guestRequest{Kind: kind, Body: raw})
	return id, writeAtomic(filepath.Join(dir, id+".json"), data, 0o644)
}

// answer reads the answer to a request into v, once there is one.
func (st *guestTool) answer(id string, v any) bool {
	data, err := os.ReadFile(filepath.Join(st.root, requestsDir, id+".answer"))
	return err == nil && json.Unmarshal(data, v) == nil
}

// drop takes a request away, with its answer.
func (st *guestTool) drop(id string) {
	os.Remove(filepath.Join(st.root, requestsDir, id+".json"))
	os.Remove(filepath.Join(st.root, requestsDir, id+".answer"))
}

// request puts a request and waits up to wait for its answer, which it reads into ans.
func (st *guestTool) request(kind string, body, ans any, wait time.Duration) (answered bool, err error) {
	id, err := st.put(kind, body)
	if err != nil {
		return false, err
	}
	defer st.drop(id)
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if st.answer(id, ans) {
			return true, nil
		}
	}
	return false, nil
}

// writeAtomic puts a file in place whole, so that whoever reads the
// directory never finds half of it.
func writeAtomic(file string, data []byte, perm os.FileMode) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// --- the daemon's side ---

// readRequests reads the requests of a kind in a services dir that have no
// answer yet — or, all, those that do as well — oldest first.
func readRequests(servicesDir, kind string, all bool) []guestRequest {
	dir := filepath.Join(servicesDir, requestsDir)
	entries, _ := os.ReadDir(dir)
	var reqs []guestRequest
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !all && exists(filepath.Join(dir, id+".answer")) {
			continue
		}
		var req guestRequest
		if json.Unmarshal([]byte(readFile(filepath.Join(dir, e.Name()))), &req) == nil && req.Kind == kind {
			req.ID = id
			reqs = append(reqs, req)
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].ID < reqs[j].ID })
	return reqs
}

// answerRequest puts the answer to a request in place, whole, and for the
// container's user alone: it may hold a key.
func answerRequest(servicesDir, id string, ans any) error {
	data, _ := json.Marshal(ans)
	return writeAtomic(filepath.Join(servicesDir, requestsDir, id+".answer"), data, 0o600)
}
