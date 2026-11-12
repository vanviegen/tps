//go:build linux

// Command replay stands in for claude while the demo video is shot (see
// demo/video/README.md). Installed as the toolbox's bin/claude, with the real
// one beside it as claude.real, it runs that one with ANTHROPIC_BASE_URL
// pointing at a proxy of its own. The proxy answers each request for a model
// response from a tape when it has one, at the pace it was recorded, and
// forwards it to the API otherwise, taping the answer. Everything else claude
// does is real: its tools run, files change, services start; only what the
// model says comes from tape, which is what makes the shoot come out the same
// every time.
//
// A request is known by the model, the text the user started the conversation
// with, and how many answers the conversation holds so far: the nth answer of
// a conversation is its nth tape, however long the turns in between took.
// Commit ids in that text are left out: TPS names the commit a reviewer's
// work sits on, which is another one whenever another task merged first.
//
// Tapes are read from replay/ beside bin/, and written there too where that
// is writable (the host), or else to $CLAUDE_CONFIG_DIR/replay (a task's
// container, where the toolbox is read-only), from where the shoot collects
// them.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type tape struct {
	About  string  `json:"about"` // how the conversation starts, to tell tapes apart by
	Status int     `json:"status"`
	Type   string  `json:"type"`   // the Content-Type
	Chunks []chunk `json:"chunks"` // the body, line by line as it arrived
}

type chunk struct {
	MS   int64  `json:"ms"` // since the request was made
	Data string `json:"data"`
}

var store, out string

var commitID = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	bin := filepath.Dir(exe)
	store = filepath.Join(bin, "..", "replay")
	out = store
	if syscall.Access(store, 2) != nil { // W_OK
		out = filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "replay")
	}
	upstream := strings.TrimSuffix(os.Getenv("ANTHROPIC_BASE_URL"), "/")
	if upstream == "" {
		upstream = "https://api.anthropic.com"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serve(w, r, upstream) }))

	cmd := exec.Command(filepath.Join(bin, "claude.real"), os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL=http://"+ln.Addr().String())
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		fail(err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	var exit *exec.ExitError
	if err := cmd.Wait(); errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
}

func fail(err error) {
	os.Stderr.WriteString("replay: " + err.Error() + "\n")
	os.Exit(1)
}

func serve(w http.ResponseWriter, r *http.Request, upstream string) {
	body, _ := io.ReadAll(r.Body)
	key, about := "", ""
	if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
		key, about = keyOf(body)
	}
	if key != "" {
		if t, ok := load(key); ok {
			play(w, t)
			return
		}
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range r.Header {
		if k != "Host" && k != "Content-Length" && k != "Accept-Encoding" {
			req.Header[k] = v
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	for k, v := range res.Header {
		if k != "Content-Length" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(res.StatusCode)
	t := tape{About: about, Status: res.StatusCode, Type: res.Header.Get("Content-Type")}
	start := time.Now()
	rd := bufio.NewReader(res.Body)
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			t.Chunks = append(t.Chunks, chunk{time.Since(start).Milliseconds(), line})
			if _, werr := io.WriteString(w, line); werr != nil {
				return // claude hung up: nothing whole to tape
			}
			w.(http.Flusher).Flush()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return
		}
	}
	if key != "" && res.StatusCode == http.StatusOK {
		save(key, t)
	}
}

func play(w http.ResponseWriter, t tape) {
	w.Header().Set("Content-Type", t.Type)
	w.WriteHeader(t.Status)
	start := time.Now()
	for _, c := range t.Chunks {
		time.Sleep(time.Until(start.Add(time.Duration(c.MS) * time.Millisecond)))
		if _, err := io.WriteString(w, c.Data); err != nil {
			return
		}
		w.(http.Flusher).Flush()
	}
}

// keyOf names a request for a model response: the model, the user's words
// that open the conversation (claude's system reminders around them change
// from day to day, so they are left out), and the number of answers so far;
// and the first of those words, for a person to know the tape by.
func keyOf(body []byte) (key, about string) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 {
		return "", ""
	}
	h := sha256.New()
	h.Write([]byte(req.Model + "\x00"))
	var text string
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(req.Messages[0].Content, &text) == nil {
		blocks = append(blocks, struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"text", text})
	} else {
		_ = json.Unmarshal(req.Messages[0].Content, &blocks)
	}
	for _, b := range blocks {
		if b.Type == "text" && !strings.HasPrefix(strings.TrimSpace(b.Text), "<system-reminder>") {
			h.Write([]byte(commitID.ReplaceAllString(b.Text, "#") + "\x00"))
			about += b.Text + " "
		}
	}
	answers := 0
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			answers++
		}
	}
	if r := []rune(strings.TrimSpace(about)); len(r) > 100 {
		about = string(r[:100]) + "…"
	}
	return hex.EncodeToString(h.Sum(nil))[:16] + "-" + strconv.Itoa(answers), strings.TrimSpace(about)
}

func load(key string) (t tape, ok bool) {
	for _, dir := range []string{out, store} {
		if raw, err := os.ReadFile(filepath.Join(dir, key+".json")); err == nil && json.Unmarshal(raw, &t) == nil {
			return t, true
		}
	}
	return t, false
}

func save(key string, t tape) {
	raw, _ := json.MarshalIndent(t, "", "\t")
	_ = os.MkdirAll(out, 0o755)
	tmp := filepath.Join(out, "."+key+".tmp")
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(out, key+".json"))
	}
}
