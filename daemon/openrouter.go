package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// An agent may ask for OpenRouter spending in its TPS-DONE line (see Request):
// to reach models its own agent does not offer, or to test something that
// calls an LLM API. What it gets is a key of its own, made on this host's
// OpenRouter account with the management key the user set for the host, and
// limited to what it asked for. The task's OpenRouter budget is how much of
// that may be handed out without asking; a request beyond it waits for the
// user, who may raise the budget or turn the request down.

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

// createOpenRouterKey makes a key limited to spending limit USD, and returns it.
func createOpenRouterKey(mgmtKey, name string, limit float64) (string, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "limit": limit})
	req, _ := http.NewRequest("POST", "https://openrouter.ai/api/v1/keys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mgmtKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := openRouterClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Key   string `json:"key"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode/100 != 2 || out.Key == "" {
		return "", fmt.Errorf("OpenRouter answered %s %s", resp.Status, out.Error.Message)
	}
	return out.Key, nil
}

// orRoomL is what is left of the task's OpenRouter budget.
func (t *Task) orRoomL() float64 {
	if t.info.ORBudget == nil {
		return -t.info.ORGranted
	}
	return *t.info.ORBudget - t.info.ORGranted
}

// requestOpenRouter answers an agent's turn that ended asking for amount USD:
// a key, when the budget has room for it, and otherwise the question for the
// user, with the task theirs until it is answered.
func (t *Task) requestOpenRouter(amount float64, changes string) {
	t.lock()
	if t.p.m.orKey == "" {
		t.unlock()
		t.note("the agent asked for OpenRouter access, which this host has no management key for")
		t.kick(openRouterMissingPrompt)
		return
	}
	if amount > t.orRoomL()+0.005 {
		t.note(fmt.Sprintf("the agent asks for $%g of OpenRouter spending, more than the task's OpenRouter budget has left", amount))
		t.endRunL(changes)
		t.lock()
		t.info.ORAsk = amount
		t.p.m.saveL()
		t.publishL()
		t.unlock()
		return
	}
	t.unlock()
	t.grantOpenRouter(amount)
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
		return fmt.Errorf("That budget leaves $%.2f for a request of $%g", max(room, 0), amount)
	}
	t.info.ORAsk = 0
	t.p.m.saveL()
	t.publishL()
	t.unlock()
	go t.grantOpenRouter(amount)
	return nil
}

// RejectOpenRouter is the user turning the request down.
func (t *Task) RejectOpenRouter() error {
	t.lock()
	if t.info.ORAsk == 0 {
		t.unlock()
		return errors.New("The agent's request has been answered already")
	}
	t.info.ORAsk = 0
	t.p.m.saveL()
	t.publishL()
	t.unlock()
	t.note("OpenRouter request rejected")
	t.kick(openRouterRejectedPrompt)
	return nil
}

func (t *Task) grantOpenRouter(amount float64) {
	t.lock()
	mgmtKey := t.p.m.orKey
	name := fmt.Sprintf("TPS %s: %s", t.p.info.Name, oneLine(t.info.Title, 40))
	t.unlock()
	key, err := createOpenRouterKey(mgmtKey, name, amount)
	if err != nil {
		t.noteErr("making an OpenRouter key failed", err)
		t.kick(openRouterFailedPrompt(err))
		return
	}
	t.lock()
	t.info.ORGranted += amount
	t.p.m.saveL()
	t.publishL()
	t.unlock()
	t.note(fmt.Sprintf("handed the agent an OpenRouter key for $%g", amount))
	t.kick(openRouterKeyPrompt(key, amount))
}
