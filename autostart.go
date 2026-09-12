package main

import (
	"os"
	"path/filepath"
	"strings"
)

// autostart writes the XDG autostart entry that runs this binary, from where
// it is now, with --no-open at every login: what `tps --autostart` does
// before it goes on to run as usual. Returns the entry's path.
func autostart() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	confDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(confDir, "autostart")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "tps.desktop")
	entry := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=TPS\n" +
		"Comment=Kanban manager for AI coding agents\n" +
		"Exec=" + desktopQuote(exe) + " --no-open\n" +
		"Terminal=false\n" +
		"StartupNotify=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return path, os.WriteFile(path, []byte(entry), 0o644)
}

// desktopQuote quotes an Exec argument the way the desktop entry spec wants
// it, and only when it needs quoting.
func desktopQuote(arg string) string {
	if !strings.ContainsAny(arg, " \t\n\"'\\><~|&;$*?#()`") {
		return arg
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(arg) + `"`
}
