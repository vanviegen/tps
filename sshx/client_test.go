package sshx

import "testing"

func TestControlPathSet(t *testing.T) {
	if controlPathSet("user frank\ncontrolmaster false\n") || controlPathSet("controlpath none\n") || !controlPathSet("controlpath /tmp/ssh_mux_%h\n") {
		t.Error("controlPathSet")
	}
	if Host("-p 2222 -J jump alice@box") != "alice@box" || Host("box") != "box" {
		t.Error("Host")
	}
}
