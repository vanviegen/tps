package ui

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Questions for the user (SSH passwords, host key confirmations) are
// published as state under 'ask' and answered with the 'answer' command.
// The answer itself never enters the state tree.

type answer struct {
	value  string
	cancel bool
}

func (u *UI) Ask(title, text, kind string) (string, error) {
	u.askMu.Lock()
	u.askNext++
	id := u.askNext
	ch := make(chan answer, 1)
	u.asks[id] = ch
	u.askMu.Unlock()
	key := strconv.Itoa(id)
	u.hub.Set([]string{"ask", key}, map[string]any{"title": title, "text": text, "kind": kind})
	defer func() {
		u.hub.Set([]string{"ask", key}, nil)
		u.askMu.Lock()
		delete(u.asks, id)
		u.askMu.Unlock()
	}()
	select {
	case a := <-ch:
		if a.cancel {
			return "", errors.New("cancelled")
		}
		return a.value, nil
	case <-time.After(5 * time.Minute):
		return "", errors.New("no answer")
	}
}

func (u *UI) answer(raw json.RawMessage) (any, error) {
	var args struct {
		ID     json.Number `json:"id"`
		Value  string      `json:"value"`
		Cancel bool        `json:"cancel"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, errors.New("bad arguments")
	}
	id, _ := args.ID.Int64()
	u.askMu.Lock()
	ch := u.asks[int(id)]
	u.askMu.Unlock()
	if ch == nil {
		return nil, errors.New("nothing to answer")
	}
	select {
	case ch <- answer{value: args.Value, cancel: args.Cancel}:
	default:
	}
	return nil, nil
}
