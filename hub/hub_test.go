package hub

import (
	"encoding/json"
	"strings"
	"testing"
)

func drain(c *Client) []string {
	var out []string
	for {
		select {
		case raw := <-c.Out:
			out = append(out, string(raw))
		default:
			return out
		}
	}
}

// A client sees the hello, patches (deduped), chat replay and command replies.
func TestHub(t *testing.T) {
	h := New(map[string]any{"projects": map[string]any{}})
	h.Cmds["echo"] = func(args json.RawMessage) (any, error) { return map[string]any{"got": json.RawMessage(args)}, nil }
	watched := map[string]int{}
	h.OnWatch = func(key string, n int) { watched[key] = n }
	c := h.AddClient()
	if msgs := drain(c); len(msgs) != 1 || !strings.HasPrefix(msgs[0], `{"hello":`) {
		t.Fatalf("hello: %v", msgs)
	}
	h.Set([]string{"projects", "p", "name"}, "x")
	h.Set([]string{"projects", "p", "name"}, "x") // no change, no message
	h.Set([]string{"projects", "p", "name"}, nil)
	h.Set([]string{"projects", "p", "name"}, nil)
	if msgs := drain(c); len(msgs) != 2 || msgs[0] != `{"p":["projects","p","name"],"v":"x"}` || msgs[1] != `{"del":true,"p":["projects","p","name"]}` {
		t.Errorf("patches: %v", msgs)
	}
	if h.Get("projects", "p") == nil || h.Get("nope", "x") != nil {
		t.Error("get")
	}
	h.Chat("p/1", Entry{V: "a"})
	h.Chat("p/1", Entry{ID: "i", V: "b1"})
	h.ChatUpdate("p/1", Entry{ID: "i", V: "b2"})
	h.Handle(c, []byte(`{"watch":"p/1","on":true}`))
	if msgs := drain(c); len(msgs) != 1 || msgs[0] != `{"c":"p/1","es":["a","b2"]}` || watched["p/1"] != 1 {
		t.Errorf("replay: %v %v", msgs, watched)
	}
	h.Chat("p/1", Entry{V: "c"})
	h.Handle(c, []byte(`{"id":7,"cmd":"echo","args":{"k":1}}`))
	h.Handle(c, []byte(`{"id":8,"cmd":"nope"}`))
	var got []string
	for len(got) < 3 {
		got = append(got, string(<-c.Out))
	}
	if got[0] != `{"c":"p/1","e":"c"}` || !contains(got, `{"re":7,"result":{"got":{"k":1}}}`) || !contains(got, `{"error":"Unknown command: nope","re":8}`) {
		t.Errorf("stream: %v", got)
	}
	h.RemoveClient(c)
	if watched["p/1"] != 0 || h.WatcherCount("p/1") != 0 {
		t.Error("watch count after leave")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A long log keeps what the user said and its save points, the oldest of the
// rest folding into gaps that read back as such.
func TestFold(t *testing.T) {
	h := New(nil)
	h.Chat("p/1", Entry{K: "mark", V: "start"})
	h.Chat("p/1", Entry{K: "user", V: "do it"})
	for i := 0; i < chatKeep; i++ {
		h.Chat("p/1", Entry{K: "text", V: i})
	}
	h.Chat("p/1", Entry{K: "mark", V: "done"})
	h.Chat("p/1", Entry{K: "user", V: "more"})
	h.Chat("p/1", Entry{K: "text", V: "last"})
	log := h.chats["p/1"]
	if len(log) != 2+1+chatKeep-101+3 || log[0].V != "start" || log[1].V != "do it" || log[2].V != (Gap{K: "gap", N: 101}) || log[3].V != 101 {
		t.Fatalf("folded: %v", log[:4])
	}
	// A gap from a daemon, folded into further by the UI's hub.
	raw, _ := json.Marshal(log[2].V)
	if again := fold(append([]Entry{RawEntry(raw)}, log[3:]...)); again[0].V != (Gap{K: "gap", N: 101}) {
		t.Errorf("read back: %v", again[0])
	}
	h.SetChat("p/2", append([]Entry{RawEntry(raw)}, append(log[3:], log[3:]...)...))
	if again := h.chats["p/2"]; again[0].V != (Gap{K: "gap", N: 101 + 399}) {
		t.Errorf("refolded: %v", again[0])
	}
}
