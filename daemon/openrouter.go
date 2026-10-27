package daemon

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// An agent may ask for OpenRouter spending with 'tps-guest-tool openrouter
// <usd>': to reach models its own agent does not offer, or to test something
// that calls an LLM API. What it gets is the task's key, made on this host's
// OpenRouter account with the management key the user set for the host at the
// first request, and its limit raised by what each later one asks for, so the
// agent keeps the one key it has. The task's OpenRouter budget is how much of
// that may be handed out without asking; a request beyond it waits for the
// user, who may raise the budget or turn the request down.
//
// The tool and the daemon talk through two files in the task's services dir:
// the tool writes the amount to orRequestFile, the daemon (on its services
// tick) answers in orAnswerFile, and the tool takes both away once it has read
// the answer.
const (
	orRequestFile = ".openrouter-request"
	orAnswerFile  = ".openrouter-answer"
)

// orAnswer is what orAnswerFile holds: a key and its limit, or why there is none.
type orAnswer struct {
	Key   string  `json:"key,omitempty"`
	Limit float64 `json:"limit,omitempty"`
	Error string  `json:"error,omitempty"`
}

// setOpenRouterKey sets the management key this host's agent keys are made
// with; empty takes it away.
func (m *Manager) setOpenRouterKey(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orKey = strings.TrimSpace(key)
	m.saveL()
	m.hub.Set([]string{"openrouter"}, m.orKey != "")
}

var openRouterClient = &http.Client{Timeout: 30 * time.Second}

// orKey is a task's OpenRouter key: Hash is what OpenRouter's API names it by,
// and Limit the USD of spending it allows.
type orKey struct {
	Key   string  `json:"key"`
	Hash  string  `json:"hash"`
	Limit float64 `json:"limit"`
}

// openRouterKey makes a key limited to spending limit USD, or, given the hash
// of one made before, sets that one's limit; and returns the key and its hash
// (the key itself only when it was made).
func openRouterKey(mgmtKey, hash, name string, limit float64) (key, keyHash string, err error) {
	method, url, fields := "POST", "https://openrouter.ai/api/v1/keys", map[string]any{"name": name, "limit": limit}
	if hash != "" {
		method, url, fields = "PATCH", url+"/"+hash, map[string]any{"limit": limit}
	}
	body, _ := json.Marshal(fields)
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mgmtKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := openRouterClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var out struct {
		Key  string `json:"key"`
		Data struct {
			Hash string `json:"hash"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode/100 != 2 || hash == "" && (out.Key == "" || out.Data.Hash == "") {
		return "", "", fmt.Errorf("OpenRouter answered %s %s", resp.Status, out.Error.Message)
	}
	return out.Key, cmp.Or(hash, out.Data.Hash), nil
}

// orRoomL is what is left of the task's OpenRouter budget.
func (t *Task) orRoomL() float64 {
	if t.info.ORBudget == nil {
		return -t.info.ORGranted
	}
	return *t.info.ORBudget - t.info.ORGranted
}

// checkORRequestL takes up a request the tool left that nothing is answering
// yet: a key, when the budget has room for it, and otherwise the question for
// the user.
func (t *Task) checkORRequestL() {
	dir := t.servicesDir()
	if t.orBusy || t.info.ORAsk > 0 || exists(filepath.Join(dir, orAnswerFile)) {
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, orRequestFile))
	if err != nil {
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	switch {
	case err != nil || amount <= 0:
		writeORAnswer(dir, orAnswer{Error: "the request names no amount of USD"})
	case t.p.m.orKey == "":
		t.note("The agent asked for OpenRouter access, which this host has no management key for")
		writeORAnswer(dir, orAnswer{Error: "this host has no OpenRouter management key, so TPS cannot make keys here"})
	case amount > t.orRoomL()+0.005:
		t.note(fmt.Sprintf("The agent asks for $%g of OpenRouter spending, more than the task's OpenRouter budget has left", amount))
		t.info.ORAsk = amount
		t.p.m.saveL()
		t.publishL()
	default:
		t.orBusy = true
		go t.grantOpenRouter(amount)
	}
}

// writeORAnswer puts the answer in place whole, never half written for the tool to read.
func writeORAnswer(dir string, a orAnswer) {
	data, _ := json.Marshal(a)
	tmp := filepath.Join(dir, orAnswerFile+".tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, orAnswerFile))
	}
}

// GrantOpenRouter is the user raising the OpenRouter budget to what the
// request waiting for them needs, or more.
func (t *Task) GrantOpenRouter(budget any) error {
	t.lock()
	amount := t.info.ORAsk
	if amount == 0 {
		t.unlock()
		return errors.New("The agent's request has been answered already")
	}
	t.info.ORBudget = parseBudget(budget)
	if room := t.orRoomL(); amount > room+0.005 {
		t.unlock()
		return fmt.Errorf("That OpenRouter budget leaves $%.2f for a request of $%g", max(room, 0), amount)
	}
	t.info.ORAsk = 0
	t.orBusy = true
	t.p.m.saveL()
	t.publishL()
	t.unlock()
	go t.grantOpenRouter(amount)
	return nil
}

// RejectOpenRouter is the user turning the request down.
func (t *Task) RejectOpenRouter() error {
	t.lock()
	defer t.unlock()
	if t.info.ORAsk == 0 {
		return errors.New("The agent's request has been answered already")
	}
	t.info.ORAsk = 0
	t.p.m.saveL()
	t.publishL()
	writeORAnswer(t.servicesDir(), orAnswer{Error: "the user turned the request down"})
	t.note("OpenRouter request rejected")
	return nil
}

// grantOpenRouter raises the task's key's limit by amount, or makes the key
// when there is none yet.
func (t *Task) grantOpenRouter(amount float64) {
	t.lock()
	mgmtKey := t.p.m.orKey
	name := fmt.Sprintf("TPS %s: %s", t.p.info.Name, oneLine(t.info.Title, 40))
	k := orKey{Limit: amount}
	if t.info.ORKey != nil {
		k = *t.info.ORKey
		k.Limit += amount
	}
	t.unlock()
	key, hash, err := openRouterKey(mgmtKey, k.Hash, name, k.Limit)
	k.Key, k.Hash = cmp.Or(key, k.Key), hash
	t.lock()
	defer t.unlock()
	t.orBusy = false
	if err != nil {
		t.noteErr("Making or raising the OpenRouter key failed", err)
		writeORAnswer(t.servicesDir(), orAnswer{Error: err.Error()})
		return
	}
	t.info.ORKey = &k
	t.info.ORGranted += amount
	t.p.m.saveL()
	t.publishL()
	writeORAnswer(t.servicesDir(), orAnswer{Key: k.Key, Limit: k.Limit})
	t.note(fmt.Sprintf("The agent's OpenRouter key allows $%g more, $%g in all", amount, k.Limit))
}

// openRouter is the tool's side: ask for a key and wait for the answer. A
// request already standing is waited for rather than made again, so running
// the same command after a wait ran out picks up where it left off.
func (st *guestTool) openRouter(args []string) int {
	if len(args) != 1 {
		return st.fail(errors.New("openrouter: how many USD of spending should the key allow?"))
	}
	if amount, err := strconv.ParseFloat(args[0], 64); err != nil || amount <= 0 {
		return st.fail(fmt.Errorf("openrouter: '%s' is not an amount of USD", args[0]))
	}
	request, answer := filepath.Join(st.root, orRequestFile), filepath.Join(st.root, orAnswerFile)
	if !exists(request) {
		_ = os.Remove(answer)
		if err := os.WriteFile(request, []byte(args[0]+"\n"), 0o644); err != nil {
			return st.fail(err)
		}
	}
	for deadline := time.Now().Add(orWait); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		raw, err := os.ReadFile(answer)
		if err != nil {
			continue
		}
		_ = os.Remove(answer)
		_ = os.Remove(request)
		var a orAnswer
		if json.Unmarshal(raw, &a) != nil || a.Key == "" {
			return st.fail(fmt.Errorf("no key: %s", cmp.Or(a.Error, "the answer could not be read")))
		}
		fmt.Fprintf(os.Stderr, "The task's OpenRouter API key for https://openrouter.ai/api/v1, limited to $%g of spending in all:\n", a.Limit)
		fmt.Println(a.Key)
		return 0
	}
	fmt.Fprintf(os.Stderr, "The user has not answered the request in half an hour: carry on without the key. The request stands, and running the same command later picks the answer up.\n")
	return 124
}

// orWait is how long the tool waits for the user to answer before the agent
// carries on without the key (see BASH_MAX_TIMEOUT_MS in claude.go).
const orWait = 30 * time.Minute
