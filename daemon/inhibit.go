package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const inhibitWho = "TPS"

// While an agent works — or a task waits out a usage limit, which is a wait
// only a machine that stays awake can keep — the daemon holds a logind
// inhibitor so nothing suspends under the running work. An open dashboard is
// no reason to keep the machine up. Nothing happens on hosts without
// systemd-inhibit, or where polkit refuses it (a daemon outside a login session).
//
// The helper waits on a pipe whose write end only the daemon holds, so the
// inhibitor is released however the daemon goes away -- a descriptor the
// kernel closes for us beats any shutdown path we could write.
func (m *Manager) inhibitLoop() {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return
	}
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
		want := m.anyAwake()
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

// wakeLead is how early a machine is woken for a scheduled run, and from when
// it is kept awake for it.
const wakeLead = 2 * time.Minute

// wakeLoop sets the RTC alarm for the next scheduled run, so that a suspended
// machine wakes up for it. That takes root, which `sudo -n rtcwake` gets only
// where a sudoers rule allows it (see the README); without one, the first try
// is logged and there is no other.
func (m *Manager) wakeLoop() {
	var set time.Time
	for range time.Tick(time.Minute) {
		m.mu.Lock()
		at := m.nextRunL()
		m.mu.Unlock()
		args := []string{"-n", "rtcwake", "-m", "disable"}
		if !at.IsZero() {
			if at = at.Add(-wakeLead); !at.After(time.Now()) {
				continue // the machine is up for this one already
			}
			args = []string{"-n", "rtcwake", "-m", "no", "-t", strconv.FormatInt(at.Unix(), 10)}
		}
		if at.Equal(set) {
			continue
		}
		if out, err := exec.Command("sudo", args...).CombinedOutput(); err != nil {
			logf("scheduled runs will not wake the machine: rtcwake: %v %s", err, strings.TrimSpace(string(out)))
			return
		}
		set = at
	}
}
