package main

import (
	"html"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// autostart has this binary run, from where it is now, with --no-open (and
// --auto-update when asked) at every login: what `tps --auto-start` does
// before it goes on to run as usual. That is an XDG autostart entry, or a
// launchd agent on macOS. Returns the file written.
func autostart(autoUpdate bool) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	args := []string{"--no-open"}
	if autoUpdate {
		args = append(args, "--auto-update")
	}
	if runtime.GOOS == "darwin" {
		return launchAgent(append([]string{exe}, args...))
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
		"Exec=" + desktopQuote(exe) + " " + strings.Join(args, " ") + "\n" +
		"Terminal=false\n" +
		"StartupNotify=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return path, os.WriteFile(path, []byte(entry), 0o644)
}

// launchAgent writes the launchd agent that runs argv at every login.
func launchAgent(argv []string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "com.github.vanviegen.tps.plist")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.github.vanviegen.tps</string>
	<key>ProgramArguments</key>
	<array>
`
	for _, arg := range argv {
		plist += "\t\t<string>" + html.EscapeString(arg) + "</string>\n"
	}
	plist += `	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
	return path, os.WriteFile(path, []byte(plist), 0o644)
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
