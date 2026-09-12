package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.local/bin/tps": "/home/u/.local/bin/tps",
		"/home/my user/bin/tps":  `"/home/my user/bin/tps"`,
		`/x/a"b$c/tps`:           `"/x/a\"b\$c/tps"`,
	} {
		if got := desktopQuote(in); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
}

// The test binary stands in for tps; the config dir is a temporary one.
func TestAutostart(t *testing.T) {
	conf := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	path, err := autostart()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(conf, "autostart", "tps.desktop") {
		t.Fatalf("written to %s", path)
	}
	entry, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if !strings.HasPrefix(string(entry), "[Desktop Entry]\n") || !strings.Contains(string(entry), "Exec="+desktopQuote(exe)+" --no-open\n") {
		t.Fatalf("unexpected entry:\n%s", entry)
	}
}
