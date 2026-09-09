// Package hub is the live state protocol shared by the daemon and the UI:
// a JSON state tree mirrored by every client through path patches, chat logs
// fanned out to whoever watches a task, and commands answered by id. The
// daemon serves it over a unix socket, the UI over websockets; the UI also
// consumes it as a client of each daemon.
package hub

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"
)

// Protocol is the version of the wire protocol and state schema between a UI
// and a daemon; MinProtocol is the oldest daemon a UI still works with. Bump
// Protocol for any change a UI of the previous version could not handle, and
// MinProtocol when old daemons can no longer be served.
const (
	Protocol    = 4
	MinProtocol = 4
)

// CmdHandler runs one command; the result is sent back as the reply.
type CmdHandler func(args json.RawMessage) (any, error)

// Client is one connected peer. The transport drains Out into its socket and
// feeds incoming messages to Hub.Handle.
type Client struct {
	Out     chan []byte
	watches map[string]bool
	dead    bool
}

// Entry is a chat log entry together with its id (empty for most entries).
// A later entry with a known id replaces the earlier one.
type Entry struct {
	ID string
	V  any
}

const chatKeep = 500 // entries held in memory (and replayed to watchers) per task

type Hub struct {
	mu      sync.Mutex
	state   map[string]any
	Cmds    map[string]CmdHandler
	OnWatch func(key string, count int) // called when the number of watchers of a task key changes
	clients map[*Client]bool
	chats   map[string][]Entry
}

func New(initial map[string]any) *Hub {
	if initial == nil {
		initial = map[string]any{}
	}
	return &Hub{state: initial, Cmds: map[string]CmdHandler{}, clients: map[*Client]bool{}, chats: map[string][]Entry{}}
}

// Set updates the state tree and broadcasts the change; nil deletes.
func (h *Hub) Set(path []string, value any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	obj := h.state
	for _, key := range path[:len(path)-1] {
		next, ok := obj[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			obj[key] = next
		}
		obj = next
	}
	last := path[len(path)-1]
	if value == nil {
		if _, ok := obj[last]; !ok {
			return
		}
		delete(obj, last)
		h.broadcast(map[string]any{"p": path, "del": true})
		return
	}
	if old, ok := obj[last]; ok {
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(value)
		if bytes.Equal(a, b) {
			return
		}
	}
	obj[last] = value
	h.broadcast(map[string]any{"p": path, "v": value})
}

func (h *Hub) Get(path ...string) any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.get(path...)
}

// Snapshot returns the subtree at path as JSON, or nil when there is none.
func (h *Hub) Snapshot(path ...string) json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.get(path...)
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	return raw
}

func (h *Hub) get(path ...string) any {
	var cur any = h.state
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

// Chat appends an entry to a task's log and streams it to the watchers.
func (h *Hub) Chat(key string, e Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendChat(key, e)
	h.toWatchers(key, map[string]any{"c": key, "e": e.V})
}

// ChatUpdate re-sends a changed entry; it replaces the newest one with the same id.
func (h *Hub) ChatUpdate(key string, e Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	log := h.chats[key]
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].ID == e.ID {
			log[i] = e
			h.toWatchers(key, map[string]any{"c": key, "e": e.V, "u": true})
			return
		}
	}
	h.appendChat(key, e)
	h.toWatchers(key, map[string]any{"c": key, "e": e.V})
}

// SetChat replaces a task's whole log (initial load, or a discard).
func (h *Hub) SetChat(key string, entries []Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(entries) > chatKeep {
		entries = entries[len(entries)-chatKeep:]
	}
	h.chats[key] = entries
	h.toWatchers(key, map[string]any{"c": key, "es": values(entries)})
}

func (h *Hub) appendChat(key string, e Entry) {
	log := append(h.chats[key], e)
	if len(log) > chatKeep {
		log = log[len(log)-chatKeep:]
	}
	h.chats[key] = log
}

func (h *Hub) WatcherCount(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.watcherCount(key)
}

func (h *Hub) watcherCount(key string) int {
	n := 0
	for c := range h.clients {
		if c.watches[key] {
			n++
		}
	}
	return n
}

func values(entries []Entry) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e.V
	}
	return out
}

func (h *Hub) broadcast(msg any) {
	raw, _ := json.Marshal(msg)
	for c := range h.clients {
		c.send(raw)
	}
}

func (h *Hub) toWatchers(key string, msg any) {
	raw, _ := json.Marshal(msg)
	for c := range h.clients {
		if c.watches[key] {
			c.send(raw)
		}
	}
}

// send never blocks: a peer that cannot keep up is dropped.
func (c *Client) send(raw []byte) {
	if c.dead {
		return
	}
	select {
	case c.Out <- raw:
	default:
		c.dead = true
		close(c.Out)
	}
}

// AddClient registers a peer and queues the hello message with the full state.
func (h *Hub) AddClient() *Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := &Client{Out: make(chan []byte, 4096), watches: map[string]bool{}}
	h.clients[c] = true
	raw, _ := json.Marshal(map[string]any{"hello": h.state})
	c.send(raw)
	return c
}

func (h *Hub) RemoveClient(c *Client) {
	h.mu.Lock()
	if !h.clients[c] {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c)
	if !c.dead {
		c.dead = true
		close(c.Out)
	}
	var changed []string
	for key := range c.watches {
		changed = append(changed, key)
	}
	counts := map[string]int{}
	for _, key := range changed {
		counts[key] = h.watcherCount(key)
	}
	h.mu.Unlock()
	if h.OnWatch != nil {
		for _, key := range changed {
			h.OnWatch(key, counts[key])
		}
	}
}

type inMsg struct {
	ID    json.RawMessage `json:"id"`
	Cmd   string          `json:"cmd"`
	Args  json.RawMessage `json:"args"`
	Watch *string         `json:"watch"`
	On    bool            `json:"on"`
}

// Handle processes one message from a client: a command (answered
// asynchronously) or a watch toggle.
func (h *Hub) Handle(c *Client, raw []byte) {
	var msg inMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	switch {
	case msg.Cmd != "":
		go h.runCmd(c, msg)
	case msg.Watch != nil:
		key := *msg.Watch
		h.mu.Lock()
		if msg.On {
			c.watches[key] = true
			replay, _ := json.Marshal(map[string]any{"c": key, "es": values(h.chats[key])})
			c.send(replay)
		} else {
			delete(c.watches, key)
		}
		count := h.watcherCount(key)
		h.mu.Unlock()
		if h.OnWatch != nil {
			h.OnWatch(key, count)
		}
	}
}

func (h *Hub) runCmd(c *Client, msg inMsg) {
	handler := h.Cmds[msg.Cmd]
	var reply map[string]any
	if handler == nil {
		reply = map[string]any{"re": msg.ID, "error": "Unknown command: " + msg.Cmd}
	} else if result, err := handler(msg.Args); err != nil {
		log.Printf("cmd %s failed: %v", msg.Cmd, err)
		reply = map[string]any{"re": msg.ID, "error": err.Error()}
	} else {
		if result == nil {
			result = true // like the JS client's cmd(): a bare success is truthy
		}
		reply = map[string]any{"re": msg.ID, "result": result}
	}
	raw, _ := json.Marshal(reply)
	h.mu.Lock()
	c.send(raw)
	h.mu.Unlock()
}

func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// WatchedKeys lists the task keys at least one client is watching.
func (h *Hub) WatchedKeys() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	var keys []string
	for c := range h.clients {
		for key := range c.watches {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	return keys
}
