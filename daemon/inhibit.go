package daemon

import (
	"os/exec"
	"sync/atomic"
	"time"
)

// While an agent works or a UI is connected, the daemon holds a logind
// inhibitor so the machine does not suspend under the running work. Nothing
// happens on hosts without systemd-inhibit, or where polkit refuses it (a
// daemon outside a login session).
func (m *Manager) inhibitLoop() {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return
	}
	var held *exec.Cmd
	var broken atomic.Bool
	for range time.Tick(5 * time.Second) {
		if broken.Load() {
			return
		}
		want := m.anyWorking() || m.hub.ClientCount() > 0
		if want && held == nil {
			cmd := exec.Command("systemd-inhibit", "--what=sleep:idle", "--who=TPS", "--why=AI agents are working", "--mode=block", "sleep", "infinity")
			if err := cmd.Start(); err != nil {
				return
			}
			held = cmd
			go func() {
				if err := cmd.Wait(); err != nil && cmd.ProcessState.ExitCode() > 0 {
					logf("suspend inhibitor unavailable: %v", err)
					broken.Store(true)
				}
			}()
		} else if !want && held != nil {
			_ = held.Process.Kill()
			held = nil
		}
	}
}
