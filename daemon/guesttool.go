package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// tps-guest-tool: what a task's agent reaches for from inside its container.
// That is the task's services — starting, stopping and watching them —
// OpenRouter keys (see openrouter.go), and questions for the user (see ask.go).
// It is the tps binary itself, copied into the toolbox under that name (see
// ensureToolbox) and dispatched on it (see GuestToolInvoked). Both the agent
// and the daemon (on the dashboard's behalf) go through it, so the two never
// disagree about what runs.
//
// A service is a directory under /services (the task's services dir,
// bind-mounted): argv (the command, NUL-separated), pid (the wrapper's), log,
// and once it ended, exit (its code) and, if it was told to stop, stopped.
// Only argv outlives the container the service ran in, which is what makes an
// ad hoc service the task's and not that container's. The daemon reads those
// files from the host side; see services.go.
//
// Starting one starts a wrapper: this binary again, in a session and process
// group of its own, that runs the command as its child, waits, and records the
// exit code. The wrapper takes TERM and lives on (a handler is not passed
// on to the child, which takes TERM the normal way), so a stop of the whole
// group still leaves a code behind.

const guestToolName = "tps-guest-tool"

// The environment variable that tells the binary it is the wrapper of a service.
const serviceWrapEnv = "TPS_SERVICE_WRAP"

// The environment variable that names the service a process runs under, for
// the questions it puts to a human (see askHuman).
const serviceEnv = "TPS_SERVICE"

const guestToolUsage = `Usage: tps-guest-tool <command> ...

Runs commands as named services of this task: detached from your shell, with
their output kept, and listed in the TPS dashboard (the play button of the
task), where the user sees them and can start, stop and read them too. Use it
for anything that serves or takes a while: dev servers, test suites, builds.

  start <name> <seconds> [command...]
                           Start a service and wait up to that many seconds for
                           it to end (0 does not wait at all). One that ends in
                           time prints its output and exits with its own code;
                           one still running exits 124, as await does, and is
                           left to it. A single command argument is run by
                           bash -c; several are argv. Without a command, it
                           runs what Containerfile.dev declares for that name
                           (its CMD is the service 'app'; a LABEL
                           tps.service.<name>="command" declares another), or
                           else what that service ran last: a name keeps its
                           command for the rest of the task, so the user can
                           start it again from the dashboard, even after the
                           container was replaced.
  await <name> [seconds]   Wait for it to end, up to 60 seconds by default.
                           Exits with the service's own exit code, or 124 if it
                           is still running then: feel free to await again.
  stop <name>              Stop it: TERM to its process group, KILL 5s later.
  restart <name>           Stop it if it runs, and start it again as before.
  destroy <name>           Stop it and take it off the list altogether, for a
                           one-off that is done with or a service started by
                           mistake. One Containerfile.dev declares stays on the
                           list, unstarted.
  logs <name> [lines]      The last lines of its output (default 100).
  ps                       Every service, with its state.

  openrouter <usd>         Not a service: ask TPS for that many USD more of
                           OpenRouter spending, for models you lack (audio,
                           video) or a system under test that calls an LLM
                           API. Prints the task's key, the same one each time,
                           its limit raised by that much. A request beyond the
                           task's OpenRouter budget waits for the user, half
                           an hour at most; one they have not answered by then
                           exits 124. Ask only once OpenRouter turns the key
                           down for want of credit.

  ask-human <question> [--option <label>]... [--input] [--secret]
            [--service <name>] [--port <port>]
                           Not a service: put a question to the user in the
                           dashboard, and wait for the answer — to have them
                           look at something or decide, or for a script that
                           needs a human's judgment or input, such as a test
                           whose screenshots someone has to accept, or a
                           deploy that wants a one-time password. Each option
                           is a button; the exit code is the index of the one
                           pressed (so the first means yes), and its label is
                           printed. --input adds a text field, whose text is
                           printed instead; --secret hides what is typed in it.
                           --service starts that service if it is not running
                           (a review app, say), and its exit code answers the
                           question if no button does first; --port links the
                           user to that port of this container. It needs
                           something to answer with: an option, the field, or
                           a service. Changes made to /work while a run of the
                           test service waits on a question are the human's,
                           and do not void the run.
`

// GuestToolInvoked tells whether this process is the guest tool: the binary
// run under the tool's name, as 'tps guest ...', or as the wrapper of a
// service. When it is, the tool runs to completion and the exit code comes
// back.
func GuestToolInvoked(args []string) (handled bool, code int) {
	if dir := os.Getenv(serviceWrapEnv); dir != "" {
		return true, serviceWrap(dir, args[1:])
	}
	if filepath.Base(args[0]) == guestToolName {
		return true, guestToolMain(args[1:])
	}
	if len(args) > 1 && args[1] == "guest" {
		return true, guestToolMain(args[2:])
	}
	return false, 0
}

// guestTool is the tool's view of one task's services directory.
type guestTool struct {
	root string
}

func guestToolMain(args []string) int {
	root := os.Getenv("TPS_SERVICES")
	if root == "" {
		root = servicesMount
	}
	st := &guestTool{root: root}
	if len(args) == 0 {
		fmt.Print(guestToolUsage)
		return 0
	}
	var err error
	switch cmd, rest := args[0], args[1:]; cmd {
	case "start":
		return st.start(rest)
	case "await":
		return st.await(rest)
	case "stop":
		err = st.stop(rest)
	case "restart":
		err = st.restart(rest)
	case "destroy":
		err = st.destroy(rest)
	case "logs":
		err = st.logs(rest)
	case "openrouter":
		return st.openRouter(rest)
	case "ask-human":
		return st.askHuman(rest)
	case "ps", "list":
		err = st.ps()
	case "help", "-h", "--help":
		fmt.Print(guestToolUsage)
	default:
		err = fmt.Errorf("unknown command '%s'; run without arguments for usage", cmd)
	}
	if err != nil {
		return st.fail(err)
	}
	return 0
}

func (st *guestTool) dir(name string) (string, error) {
	if !serviceNameRe.MatchString(name) {
		return "", fmt.Errorf("'%s' is not a service name (letters, digits, . _ -; up to 40)", name)
	}
	return filepath.Join(st.root, name), nil
}

// fail is how a command that answers with an exit code reports what went
// wrong with it, rather than with the service.
func (st *guestTool) fail(err error) int {
	fmt.Fprintf(os.Stderr, "%s: %v\n", guestToolName, err)
	return 2
}

// pid is the wrapper's pid, or 0 when the service never started.
func servicePid(dir string) int {
	pid, _ := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(dir, "pid"))))
	return pid
}

// alive: the service started, has not recorded its end, and its wrapper is there.
func serviceAlive(dir string) bool {
	pid := servicePid(dir)
	return pid > 0 && !exists(filepath.Join(dir, "exit")) && syscall.Kill(pid, 0) == nil
}

// repair records the end of a service whose wrapper went without writing
// its exit (killed outright), so it does not look running forever.
func serviceRepair(dir string) {
	if pid := servicePid(dir); pid > 0 && !exists(filepath.Join(dir, "exit")) && syscall.Kill(pid, 0) != nil {
		_ = os.WriteFile(filepath.Join(dir, "exit"), []byte("137\n"), 0o644)
	}
}

// start launches a service and then waits the caller's few seconds for it, so
// that a command short enough to have an answer is answered here and now, and
// one that is not is left running with its output kept. How long to wait has
// to be said: a test suite and a dev server are the same command to this tool,
// and only whoever starts it knows which it is.
func (st *guestTool) start(args []string) int {
	if len(args) < 2 {
		return st.fail(fmt.Errorf("start: which service, and how many seconds to wait for it? (0 not to wait)"))
	}
	name := args[0]
	seconds, err := strconv.Atoi(args[1])
	if err != nil || seconds < 0 {
		return st.fail(fmt.Errorf("start: '%s' is not a number of seconds to wait", args[1]))
	}
	if err := st.launch(name, args[2:]); err != nil {
		return st.fail(err)
	}
	dir, _ := st.dir(name) // launch had it good
	fmt.Printf("Started '%s' (pid %d): %s\n", name, servicePid(dir), serviceCmd(serviceArgv(dir)))
	return st.wait(dir, name, seconds)
}

func (st *guestTool) launch(name string, argv []string) error {
	dir, err := st.dir(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	serviceRepair(dir)
	if serviceAlive(dir) {
		return fmt.Errorf("'%s' is running already (pid %d); stop or restart it", name, servicePid(dir))
	}
	if len(argv) == 0 {
		// Declared services run what Containerfile.dev says now; an ad hoc one
		// runs again as it did, in this container or an earlier one.
		if declared := strings.TrimSpace(readFile(filepath.Join(st.root, ".declared", name))); declared != "" {
			argv = []string{declared}
		} else if argv = serviceArgv(dir); len(argv) == 0 {
			return fmt.Errorf("no command given, and Containerfile.dev declares no service '%s'", name)
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	resetService(dir)
	_ = os.WriteFile(filepath.Join(dir, "argv"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
	if name == testService {
		noteTree(dir, "start")
	}
	log, err := os.OpenFile(filepath.Join(dir, "log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	// The wrapper says when it has started the command, on a pipe of its own:
	// a stop that came before it had its signal handling in place would kill
	// the wrapper itself, and leave no exit code behind.
	ready, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer ready.Close()
	wrap := exec.Command(self, argv...)
	wrap.Env = append(termEnv(os.Environ()), serviceWrapEnv+"="+dir, serviceEnv+"="+name)
	wrap.Stdout, wrap.Stderr = log, log // stdin: /dev/null
	wrap.ExtraFiles = []*os.File{readyW}
	wrap.Dir, _ = os.Getwd()
	wrap.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := wrap.Start(); err != nil {
		readyW.Close()
		return err
	}
	readyW.Close()
	pid := wrap.Process.Pid
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644)
	_ = wrap.Process.Release()
	_ = ready.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = ready.Read(make([]byte, 1))
	return nil
}

// wait watches a service for so many seconds and reports how it went: its own
// exit code once it has one, or 124 while it is still running, which is the
// one answer that says to come back for the real one.
func (st *guestTool) wait(dir, name string, seconds int) int {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for serviceAlive(dir) {
		if !time.Now().Before(deadline) {
			fmt.Printf("'%s' is still running. Its output so far is in %s/log (%s logs %s),\n", name, dir, guestToolName, name)
			fmt.Printf("and '%s await %s <seconds>' waits for it to end.\n", guestToolName, name)
			return 124
		}
		time.Sleep(300 * time.Millisecond)
	}
	serviceRepair(dir)
	code, _ := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(dir, "exit"))))
	if exists(filepath.Join(dir, "stopped")) {
		fmt.Printf("'%s' was stopped (exit code %d).\n", name, code)
	} else {
		fmt.Printf("'%s' exited with code %d.\n", name, code)
	}
	fmt.Printf("The last lines of its output (%s/log has it all):\n", dir)
	for _, line := range lastLines(readFile(filepath.Join(dir, "log")), 30) {
		fmt.Printf("  %s\n", line)
	}
	return code
}

// termEnv describes the terminal a service writes to (see serviceWrap): an
// xterm, whatever the shell that started it had. Without a pager: one would
// wait on that terminal for a key nobody presses. A pager the environment
// names on purpose stays.
func termEnv(env []string) []string {
	env = append(withoutEnv(env, "TERM"), "TERM=xterm-256color")
	for _, name := range []string{"PAGER", "GIT_PAGER"} {
		if !hasEnv(env, name) {
			env = append(env, name+"=cat")
		}
	}
	return env
}

func hasEnv(env []string, name string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return true
		}
	}
	return false
}

// serviceWrap is the wrapper: it runs the command as its child and records
// the exit code. A single argument is a shell line. The command writes to a
// pseudo-terminal whose output the wrapper copies into the log (its own
// stdout), so it shows the colours and progress lines it would show a person
// (web/ansi.ts plays them out), and writes as it goes rather than a buffer at
// a time. Its stdin stays /dev/null, and the terminal is no controlling one:
// whatever asks a question finds nobody there, as it would without a terminal.
func serviceWrap(dir string, argv []string) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP) // taken, and ignored: the child gets its own
	if len(argv) == 1 {
		argv = []string{"bash", "-c", argv[0]}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// The wrapper flag is spent: it says "you are the wrapper of this service",
	// and the service must not hear it. It would be handed down the whole
	// process tree otherwise, and any TPS binary the service runs — this
	// project's own, under `go run .` — would take itself for the wrapper and
	// try to exec its arguments.
	cmd.Env = withoutEnv(os.Environ(), serviceWrapEnv)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	copied := make(chan struct{})
	master, slave, err := openPty()
	if err == nil {
		defer master.Close()
		cmd.Stdout, cmd.Stderr = slave, slave
		go func() { _, _ = io.Copy(os.Stdout, master); close(copied) }()
	} else {
		close(copied)
	}
	err = cmd.Start()
	if slave != nil {
		slave.Close()
	}
	if ready := os.NewFile(3, "ready"); ready != nil { // launch waits for this (see there)
		_, _ = ready.Write([]byte{1})
		ready.Close()
	}
	if err == nil {
		err = cmd.Wait()
	}
	// What the command left in the terminal goes to the log before the exit
	// code says it is complete; whatever it left running in the background
	// and still holding the terminal is not waited for.
	select {
	case <-copied:
	case <-time.After(time.Second):
	}
	code := shellExitCode(err)
	if code == 127 {
		fmt.Fprintf(os.Stderr, "%s: %v\n", guestToolName, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "exit"), []byte(strconv.Itoa(code)+"\n"), 0o644)
	return code
}

// withoutEnv is the environment with one variable taken out of it.
func withoutEnv(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// shellExitCode is the exit code a shell would report for a finished command: its
// own, 128 plus the signal that killed it, or 127 when it could not be run.
func shellExitCode(err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	default:
		return 127
	}
}

func (st *guestTool) stop(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("stop: which service?")
	}
	dir, err := st.dir(args[0])
	if err != nil {
		return err
	}
	if !exists(dir) {
		return fmt.Errorf("no service '%s'", args[0])
	}
	serviceRepair(dir)
	if !serviceAlive(dir) {
		fmt.Printf("'%s' is not running\n", args[0])
		return nil
	}
	pid := servicePid(dir)
	_ = os.WriteFile(filepath.Join(dir, "stopped"), nil, 0o644)
	signalGroup(pid, syscall.SIGTERM)
	for i := 0; i < 50 && serviceAlive(dir); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if serviceAlive(dir) {
		signalGroup(pid, syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
		if !exists(filepath.Join(dir, "exit")) {
			_ = os.WriteFile(filepath.Join(dir, "exit"), []byte("137\n"), 0o644)
		}
	}
	fmt.Printf("Stopped '%s'\n", args[0])
	return nil
}

// signalGroup signals the service's process group, or failing that its wrapper.
func signalGroup(pid int, sig syscall.Signal) {
	if syscall.Kill(-pid, sig) != nil {
		_ = syscall.Kill(pid, sig)
	}
}

func (st *guestTool) restart(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("restart: which service?")
	}
	dir, err := st.dir(args[0])
	if err != nil {
		return err
	}
	if !exists(dir) && !exists(filepath.Join(st.root, ".declared", args[0])) {
		return fmt.Errorf("no service '%s'", args[0])
	}
	if exists(dir) && serviceAlive(dir) {
		if err := st.stop(args); err != nil {
			return err
		}
	}
	if err := st.launch(args[0], nil); err != nil {
		return err
	}
	fmt.Printf("Restarted '%s'\n", args[0])
	return nil
}

// destroy takes a service off the list for good: a one-off that has served
// its purpose, or one started by mistake, which would otherwise sit in the
// dashboard for the rest of the task. A declared one is the Containerfile's
// to offer, so only the run of it goes.
func (st *guestTool) destroy(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("destroy: which service?")
	}
	name := args[0]
	dir, err := st.dir(name)
	if err != nil {
		return err
	}
	declared := exists(filepath.Join(st.root, ".declared", name))
	if !exists(dir) {
		if !declared {
			return fmt.Errorf("no service '%s'", name)
		}
		fmt.Printf("'%s' has not been started\n", name)
		return nil
	}
	if serviceAlive(dir) {
		if err := st.stop(args); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if declared {
		fmt.Printf("Destroyed the run of '%s'; Containerfile.dev declares it, so it stays on the list, unstarted.\n", name)
	} else {
		fmt.Printf("Destroyed '%s'\n", name)
	}
	return nil
}

func (st *guestTool) await(args []string) int {
	if len(args) == 0 {
		return st.fail(fmt.Errorf("await: which service?"))
	}
	name := args[0]
	dir, err := st.dir(name)
	if err != nil {
		return st.fail(err)
	}
	seconds := 60
	if len(args) > 1 {
		if seconds, err = strconv.Atoi(args[1]); err != nil || seconds < 0 {
			return st.fail(fmt.Errorf("await: '%s' is not a number of seconds", args[1]))
		}
	}
	if servicePid(dir) == 0 {
		return st.fail(fmt.Errorf("'%s' has not been started", name))
	}
	return st.wait(dir, name, seconds)
}

func (st *guestTool) logs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("logs: which service?")
	}
	dir, err := st.dir(args[0])
	if err != nil {
		return err
	}
	if !exists(filepath.Join(dir, "log")) {
		return fmt.Errorf("'%s' has not been started", args[0])
	}
	n := 100
	if len(args) > 1 {
		if n, err = strconv.Atoi(args[1]); err != nil || n < 0 {
			return fmt.Errorf("logs: '%s' is not a number of lines", args[1])
		}
	}
	for _, line := range lastLines(readFile(filepath.Join(dir, "log")), n) {
		fmt.Println(line)
	}
	return nil
}

// lastLines is the last n lines of text (a trailing newline ends no line),
// each as it was left: a progress line that rewrote itself after a carriage
// return says only what it said last.
func lastLines(text string, n int) []string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		lines[i] = line[strings.LastIndexByte(line, '\r')+1:]
	}
	return lines
}

func (st *guestTool) ps() error {
	var declared []declaredService
	if entries, _ := os.ReadDir(filepath.Join(st.root, ".declared")); len(entries) > 0 {
		for _, e := range entries {
			declared = append(declared, declaredService{e.Name(), strings.TrimSpace(readFile(filepath.Join(st.root, ".declared", e.Name())))})
		}
	}
	if entries, _ := os.ReadDir(st.root); len(entries) > 0 {
		for _, e := range entries {
			if e.IsDir() && serviceNameRe.MatchString(e.Name()) {
				serviceRepair(filepath.Join(st.root, e.Name()))
			}
		}
	}
	var out bytes.Buffer
	for _, s := range readServices(st.root, declared) {
		state := "not started"
		switch s.Status {
		case "running":
			state = fmt.Sprintf("running (pid %d)", servicePid(filepath.Join(st.root, s.Name)))
		case "exited":
			state = fmt.Sprintf("exited (%d)", *s.Code)
		case "stopped":
			state = "stopped"
		default:
			if s.Declared {
				state = "declared, not started"
			}
		}
		fmt.Fprintf(&out, "%-20s %-22s %s\n", s.Name, state, s.Cmd)
	}
	_, err := os.Stdout.Write(out.Bytes())
	return err
}
