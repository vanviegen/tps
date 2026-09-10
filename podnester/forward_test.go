package podnester

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForward(t *testing.T) {
	// A target that echoes.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	control := filepath.Join(t.TempDir(), "fwd-test")
	os.WriteFile(control, []byte("starting\n"), 0o644)
	done := make(chan error, 1)
	go func() { done <- Forward(control, "127.0.0.1:0", target.Addr().String()) }()
	// The forwarder reports the port it picked.
	var port string
	for i := 0; i < 50 && port == ""; i++ {
		data, _ := os.ReadFile(control)
		if s := string(data); len(s) > 6 && s[:6] == "port: " {
			port = s[6 : len(s)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	if port == "" || port == "0" {
		t.Fatal("no port reported")
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("hello"))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("relayed %q, %v", buf, err)
	}
	conn.Close()
	// Removing the control file stops it.
	os.Remove(control)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("forwarder ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the forwarder did not stop when its control file went")
	}
	// A port in use is reported.
	busy := filepath.Join(t.TempDir(), "fwd-busy")
	if err := Forward(busy, target.Addr().String(), "127.0.0.1:1"); err == nil {
		t.Error("listening on a busy port succeeded")
	}
	if data, _ := os.ReadFile(busy); len(data) < 7 || string(data[:7]) != "error: " {
		t.Errorf("busy port reported as %q", data)
	}
	// What running siblings ask for.
	w := wantedForwarders(portBindings{"5432/tcp": {{HostIP: "", HostPort: "5432"}, {HostIP: "127.0.0.1", HostPort: "0"}}, "53/udp": {{HostPort: "53"}}}, "10.1.2.3")
	if len(w) != 2 || w[":5432"] != "10.1.2.3:5432" || w["127.0.0.1:0"] != "10.1.2.3:5432" {
		t.Errorf("wantedForwarders: %v", w)
	}
}
