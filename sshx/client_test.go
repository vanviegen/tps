package sshx

import "testing"

func TestControlPathSet(t *testing.T) {
	if controlPathSet("user frank\ncontrolmaster false\n") || controlPathSet("controlpath none\n") || !controlPathSet("controlpath /tmp/ssh_mux_%h\n") {
		t.Error("controlPathSet")
	}
	c := &Client{Dest: "-p 2222 -J jump alice@box", dest: []string{"-p", "2222", "-J", "jump", "alice@box"}}
	if c.Host() != "alice@box" {
		t.Error("Host()")
	}
}
