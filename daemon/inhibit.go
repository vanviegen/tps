package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const inhibitWho = "TPS"

// While an agent works, the daemon holds a logind inhibitor so the machine
// does not suspend under the running work. An open dashboard is no reason to
// keep the machine up. Nothing happens on hosts without systemd-inhibit, or
// where polkit refuses it (a daemon outside a login session).
//
// The helper waits on a pipe whose write end only the daemon holds, so the
// inhibitor is released however the daemon goes away -- a descriptor the
// kernel closes for us beats any shutdown path we could write.
func (m *Manager) inhibitLoop() {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return
	}
	killStaleInhibitors(inhibitWho)
	var release *os.File
	var gone *atomic.Bool // raised when the helper died on its own
	var broken atomic.Bool
	for range time.Tick(5 * time.Second) {
		if broken.Load() {
			return
		}
		if release != nil && gone.Load() {
			release.Close()
			release = nil
		}
		want := m.anyWorking()
		if want && release == nil {
			r, w, err := os.Pipe()
			if err != nil {
				return
			}
			cmd := exec.Command("systemd-inhibit", "--what=sleep:idle", "--who="+inhibitWho, "--why=AI agents are working", "--mode=block", "cat")
			cmd.Stdin = r
			err = cmd.Start()
			r.Close()
			if err != nil {
				w.Close()
				return
			}
			release, gone = w, new(atomic.Bool)
			go func(gone *atomic.Bool) {
				if err := cmd.Wait(); err != nil && cmd.ProcessState.ExitCode() > 0 {
					logf("suspend inhibitor unavailable: %v", err)
					broken.Store(true)
				}
				gone.Store(true) // killed from under us: the loop takes it again
			}(gone)
		} else if !want && release != nil {
			release.Close() // cat reads EOF and systemd-inhibit follows it out
			release = nil
		}
	}
}

// Daemons before this one left their helper running when they died, so a
// machine that restarts the daemon often collects a row of "TPS" entries in
// the desktop's power dialog. Take out whatever is still holding one of ours,
// along with the process it was babysitting.
func killStaleInhibitors(who string) {
	uid := os.Getuid()
	children := map[int][]int{}
	var stale []int
	for _, dir := range procDirs() {
		pid, err := strconv.Atoi(filepath.Base(dir))
		if err != nil {
			continue
		}
		if ppid, ok := procParent(dir); ok {
			children[ppid] = append(children[ppid], pid)
		}
		if !isOurInhibitor(dir, uid, who) {
			continue
		}
		stale = append(stale, pid)
	}
	for _, pid := range stale {
		for _, child := range children[pid] {
			syscall.Kill(child, syscall.SIGKILL)
		}
		syscall.Kill(pid, syscall.SIGKILL)
	}
	if len(stale) > 0 {
		logf("Released %d suspend inhibitor(s) left by an earlier daemon", len(stale))
	}
}

func procDirs() []string {
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	return dirs
}

// isOurInhibitor reports whether a /proc entry is a systemd-inhibit of ours
// holding the TPS inhibitor.
func isOurInhibitor(dir string, uid int, who string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != uid {
		return false
	}
	cmdline, err := os.ReadFile(dir + "/cmdline")
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
	return len(args) > 0 && filepath.Base(args[0]) == "systemd-inhibit" && slices.Contains(args, "--who="+who)
}

// procParent reads the parent pid out of /proc/<pid>/stat, whose second field
// is the (possibly space-ridden) command name in parentheses.
func procParent(dir string) (int, bool) {
	data, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return 0, false
	}
	i := strings.LastIndex(string(data), ")")
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data)[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	return ppid, err == nil
}
