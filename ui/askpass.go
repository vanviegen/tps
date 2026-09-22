package ui

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The askpass server: ssh processes spawned by the UI are given
// SSH_ASKPASS=<this binary> and a socket to reach us, so every prompt ssh
// raises — a password, a key passphrase, an unknown host key — arrives here.
//
// We never keep an ssh process waiting on a human: it is told to give up, and
// the question is published on the host it is about, where it stays put until
// it is answered. Answering remembers the answer and wakes the host's link,
// which dials again and this time has what ssh asks for. So a host that needs
// a login says exactly that, steadily, instead of retrying and re-prompting
// behind the user's back.

type askpassServer struct {
	sock    string
	mu      sync.Mutex
	secrets map[string]string // answers to give ssh, by host id and prompt
	given   map[string]string // and the ssh process each was last given to
}

// AskpassRequest is what `tps` in askpass mode sends us.
type AskpassRequest struct {
	Host   string `json:"host"`
	Run    string `json:"run"`
	Prompt string `json:"prompt"`
}

type AskpassReply struct {
	Answer string `json:"answer"`
	Cancel bool   `json:"cancel,omitempty"`
}

func (u *UI) startAskpass() (*askpassServer, error) {
	dir, err := os.MkdirTemp("", "tps-askpass-")
	if err != nil {
		return nil, err
	}
	s := &askpassServer{sock: filepath.Join(dir, "sock"), secrets: map[string]string{}, given: map[string]string{}}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(u, conn)
		}
	}()
	return s, nil
}

// Env is what an ssh process needs to route its prompts here.
func (s *askpassServer) Env(hid, run string) []string {
	exe, _ := os.Executable()
	return []string{"SSH_ASKPASS=" + exe, "SSH_ASKPASS_REQUIRE=force", "TPS_ASKPASS_SOCK=" + s.sock, "TPS_ASKPASS_HOST=" + hid, "TPS_ASKPASS_RUN=" + run}
}

func (s *askpassServer) serve(u *UI, conn net.Conn) {
	defer conn.Close()
	var req AskpassRequest
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &req) != nil {
		return
	}
	reply := s.answer(u, req)
	raw, _ := json.Marshal(reply)
	_, _ = conn.Write(append(raw, '\n'))
}

func (s *askpassServer) answer(u *UI, req AskpassRequest) AskpassReply {
	prompt := strings.TrimSpace(req.Prompt)
	key := req.Host + "\x00" + prompt
	s.mu.Lock()
	// The same process asking twice for the same thing means what we gave it
	// was refused; it is no good for the next attempt either.
	refused := s.given[key] == req.Run
	secret, have := s.secrets[key]
	if refused {
		delete(s.secrets, key)
		have = false
	}
	if have {
		s.given[key] = req.Run
	}
	s.mu.Unlock()
	if have {
		return AskpassReply{Answer: secret}
	}
	dest := req.Host
	if l, err := u.link(req.Host); err == nil {
		if t, ok := l.tr.(*sshTransport); ok {
			dest = t.c.Host()
		}
	}
	if strings.Contains(prompt, "(yes/no") { // an unknown host key
		u.askHost(req.Host, key, "Unknown host "+dest, prompt, "confirm")
	} else if refused {
		u.askHost(req.Host, key, "SSH login to "+dest, "That did not work. "+prompt, "password")
	} else {
		u.askHost(req.Host, key, "SSH login to "+dest, prompt, "password")
	}
	return AskpassReply{Cancel: true}
}

// askHost puts a question to the user, on the host it is about. The one that
// is already waiting there stands: ssh asking again after being turned down
// is the same question, not a second one.
func (u *UI) askHost(hid, key, title, text, kind string) {
	l, err := u.link(hid)
	if err != nil {
		return
	}
	l.mu.Lock()
	fresh := l.ask == nil
	if fresh {
		l.askKey = key
		l.ask = map[string]any{"title": title, "text": text, "kind": kind}
	}
	l.mu.Unlock()
	if fresh {
		l.publishHost()
	}
}

// answer settles the question a host waits on: the answer is remembered for
// the ssh processes to come, and the host connects again with it.
func (u *UI) answer(raw json.RawMessage) (any, error) {
	var args struct {
		Hid   string `json:"hid"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, errors.New("bad arguments")
	}
	l, err := u.link(args.Hid)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	key, ask := l.askKey, l.ask
	l.mu.Unlock()
	if ask == nil {
		return nil, errors.New("nothing to answer")
	}
	value := args.Value
	if ask["kind"] == "confirm" { // ssh wants the word, not the click
		value = "yes"
	}
	s := u.askpass
	s.mu.Lock()
	s.secrets[key] = value
	s.mu.Unlock()
	l.Wake()
	return nil, nil
}

// Askpass is the client side: run as `tps <prompt>` by ssh, it fetches the
// answer from the UI that spawned ssh and prints it.
func Askpass(sock, host, run, prompt string) int {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return 1
	}
	defer conn.Close()
	raw, _ := json.Marshal(AskpassRequest{Host: host, Run: run, Prompt: prompt})
	if _, err := conn.Write(append(raw, '\n')); err != nil {
		return 1
	}
	var reply AskpassReply
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &reply) != nil || reply.Cancel {
		return 1
	}
	os.Stdout.WriteString(reply.Answer + "\n")
	return 0
}
