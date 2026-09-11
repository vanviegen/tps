package daemon

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Running the project. Its Containerfile.dev says how with a CMD line, as any
// image does. TPS doesn't let podman run that (the container runs code-server)
// but starts it in the task's container on request, in the container's own
// environment. The ports its EXPOSE lines name are published as the container
// starts (see ensureContainer), whether the CMD runs or not. The output goes
// to the state tree, not to the chat log.

const runLogMax = 32 << 10

// runScript runs "$@" as a job of its own (job control on while starting it,
// so it leads its own process group; off again, so bash doesn't report its end
// in the output), records its pid for Stop and passes on its exit status. A
// pid left by a run that outlived a daemon restart is killed first.
const runScript = `
p=$(cat /tmp/tps-run.pid 2>/dev/null) && [ -n "$p" ] && kill -KILL -- -"$p" 2>/dev/null
set -m
"$@" &
p=$!
set +m
echo "$p" >/tmp/tps-run.pid
wait "$p"; s=$?
rm -f /tmp/tps-run.pid
exit $s
`

const runKillScript = `
p=$(cat /tmp/tps-run.pid 2>/dev/null) && [ -n "$p" ] || exit 0
kill -TERM -- -"$p" 2>/dev/null || exit 0
for i in $(seq 1 50); do kill -0 "$p" 2>/dev/null || exit 0; sleep 0.1; done
kill -KILL -- -"$p" 2>/dev/null || true
`

var instructionRe = regexp.MustCompile(`^(?i)(CMD|FROM)\s+(.*)$`)

// containerfileCmd is the CMD of the image the Containerfile describes: the
// last one of its final stage, in exec form (a shell-form CMD becomes
// ["/bin/sh","-c",…], as it does in the image). nil when there is none.
func containerfileCmd(text string) []string {
	var argv []string
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\\\r\n", ""), "\\\n", "")
	for _, line := range strings.Split(text, "\n") {
		m := instructionRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if strings.EqualFold(m[1], "FROM") {
			argv = nil
			continue
		}
		arg := strings.TrimSpace(m[2])
		var parsed []string
		if strings.HasPrefix(arg, "[") && json.Unmarshal([]byte(arg), &parsed) == nil {
			argv = parsed
		} else {
			argv = []string{"/bin/sh", "-c", arg}
		}
	}
	if len(argv) == 0 {
		return nil
	}
	return argv
}

// cmdDisplay is the command the way a user would type it.
func cmdDisplay(argv []string) string {
	if len(argv) == 3 && (argv[0] == "/bin/sh" || argv[0] == "sh") && argv[1] == "-c" {
		return argv[2]
	}
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = a
		if a == "" || strings.ContainsAny(a, " \t\"'$") {
			parts[i] = strconv.Quote(a)
		}
	}
	return strings.Join(parts, " ")
}

type runSession struct {
	c       *Container
	cmd     *exec.Cmd
	onLog   func(tail string)
	onExit  func(s *runSession, code int)
	exited  chan struct{}
	stopped atomic.Bool // ended on request: nothing to report

	mu      sync.Mutex
	log     []byte // the last runLogMax bytes of output
	pending bool   // a publish is scheduled
}

func newRunSession(c *Container, argv []string, onLog func(string), onExit func(*runSession, int)) *runSession {
	s := &runSession{c: c, onLog: onLog, onExit: onExit, exited: make(chan struct{})}
	s.cmd = exec.Command("podman", append([]string{"exec", c.Name, "bash", "-lc", runScript, "tps-run"}, argv...)...)
	w := writerFunc(s.append)
	s.cmd.Stdout, s.cmd.Stderr = w, w
	return s
}

// start spawns the process. exited closes once it is gone and onExit has run.
func (s *runSession) start() error {
	if err := s.cmd.Start(); err != nil {
		close(s.exited)
		return err
	}
	go func() {
		code := exitCode(s.cmd.Wait())
		s.flush()
		s.onExit(s, code)
		close(s.exited)
	}()
	return nil
}

func (s *runSession) append(chunk string) {
	s.mu.Lock()
	s.log = append(s.log, chunk...)
	if len(s.log) > runLogMax {
		cut := len(s.log) - runLogMax
		if i := bytes.IndexByte(s.log[cut:], '\n'); i >= 0 {
			cut += i + 1
		}
		s.log = s.log[cut:]
	}
	fire := !s.pending
	s.pending = true
	s.mu.Unlock()
	if fire {
		time.AfterFunc(200*time.Millisecond, s.flush)
	}
}

// flush publishes the tail. Under the mutex, so publishes stay in order.
func (s *runSession) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = false
	s.onLog(string(s.log))
}

// Stop ends the run: TERM to its process group from inside the container (the
// exec client can't signal in), and failing that, the client itself.
func (s *runSession) Stop() {
	s.stopped.Store(true)
	_ = s.c.Exec(runKillScript)
	select {
	case <-s.exited:
		return
	case <-time.After(2 * time.Second):
	}
	s.Kill()
	<-s.exited
}

// Kill ends just the client process (the container side dies with the container).
func (s *runSession) Kill() {
	s.stopped.Store(true)
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}
