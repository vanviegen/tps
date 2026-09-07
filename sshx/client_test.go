package sshx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	key := filepath.Join(home, ".ssh", "mykey")
	_ = os.WriteFile(key, []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host box\n\tHostName 10.0.0.5\n\tUser bob\n\tPort 2200\n\tIdentityFile ~/.ssh/mykey\n"), 0o600)
	tg, err := resolve("box")
	if err != nil || tg.host != "10.0.0.5" || tg.user != "bob" || tg.port != "2200" || len(tg.identityFiles) != 1 || tg.identityFiles[0] != key {
		t.Errorf("alias: %+v %v", tg, err)
	}
	tg, _ = resolve("alice@example.org:2222")
	if tg.host != "example.org" || tg.user != "alice" || tg.port != "2222" || len(tg.identityFiles) != 0 {
		t.Errorf("explicit: %+v", tg)
	}
	if _, err := resolve("bob@"); err == nil {
		t.Error("empty host accepted")
	}
}
