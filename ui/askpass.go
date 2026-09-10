package ui

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The askpass server: ssh processes spawned by the UI are given
// SSH_ASKPASS=<this binary> and a socket to reach us; each prompt ssh raises
// arrives here and is put to the user, unless a remembered password fits.

type askpassServer struct {
	sock    string
	mu      sync.Mutex
	secrets map[string]string // remembered answers, by host id and prompt
	asked   map[string]bool   // run ids that already used a remembered answer
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
	s := &askpassServer{sock: filepath.Join(dir, "sock"), secrets: map[string]string{}, asked: map[string]bool{}}
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
	dest := req.Host
	if l, err := u.link(req.Host); err == nil {
		if t, ok := l.tr.(*sshTransport); ok {
			dest = t.c.Host()
		}
	}
	prompt := strings.TrimSpace(req.Prompt)
	if strings.Contains(prompt, "(yes/no") { // host key confirmation
		if _, err := u.Ask(req.Host, "Unknown host "+dest, prompt, "confirm"); err != nil {
			return AskpassReply{Cancel: true}
		}
		return AskpassReply{Answer: "yes"}
	}
	// A remembered answer is tried once per ssh process; a second prompt from
	// the same process means it was wrong.
	key := req.Host + "\x00" + prompt
	s.mu.Lock()
	remembered, first := s.secrets[key], !s.asked[req.Run]
	s.asked[req.Run] = true
	s.mu.Unlock()
	if first && remembered != "" {
		return AskpassReply{Answer: remembered}
	}
	text := prompt
	if !first {
		text = "That did not work. " + prompt
	}
	answer, err := u.Ask(req.Host, "SSH login to "+dest, text, "password")
	if err != nil {
		return AskpassReply{Cancel: true}
	}
	s.mu.Lock()
	s.secrets[key] = answer
	s.mu.Unlock()
	return AskpassReply{Answer: answer}
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
