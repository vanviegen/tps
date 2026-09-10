package ui

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Questions for the user (SSH passwords, host key confirmations) are
// published as state under 'ask' and answered with the 'answer' command.
// The answer itself never enters the state tree. A question belongs to the
// host it is asked about, which is where the dashboard shows it: a login
// prompt waits in that host's box instead of interrupting whatever the user
// is doing.

type answer struct {
	value  string
	cancel bool
}

type question struct {
	hid string
	ch  chan answer
}

func (u *UI) Ask(hid, title, text, kind string) (string, error) {
	u.askMu.Lock()
	// One question per host at a time: a new one means the ssh process that
	// asked the previous is gone (it timed out, or was cancelled and retried),
	// so retire that one rather than stack two prompts on one box.
	for _, q := range u.asks {
		if q.hid == hid {
			select {
			case q.ch <- answer{cancel: true}:
			default:
			}
		}
	}
	u.askNext++
	id := u.askNext
	ch := make(chan answer, 1)
	u.asks[id] = &question{hid: hid, ch: ch}
	u.askMu.Unlock()
	key := strconv.Itoa(id)
	u.hub.Set([]string{"ask", key}, map[string]any{"title": title, "text": text, "kind": kind, "host": hid})
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
	q := u.asks[int(id)]
	u.askMu.Unlock()
	if q == nil {
		return nil, errors.New("nothing to answer")
	}
	select {
	case q.ch <- answer{value: args.Value, cancel: args.Cancel}:
	default:
	}
	return nil, nil
}
