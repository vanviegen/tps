package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Services: the named, long-running commands of a task — the project's dev
// server, its test suite, a screenshot review app — each run detached in the
// task's container by /tps/bin/tps-service-manager (see servicetool.go),
// whether the agent or the dashboard asked for it. Containerfile.dev declares
// the ones a project comes with: its CMD is the service 'app', and a LABEL
// tps.service.<name>="command" declares another. Ad hoc ones need no
// declaration. Every service is a directory under the task's services dir,
// mounted at /services in the container; the daemon writes the declared
// commands there (.declared/<name>) and reads the state and output the tool
// leaves, so the dashboard follows without a channel into the container. The
// services dir outlives the container: a replaced one takes the running
// processes with it, but what each service runs stays behind, so an ad hoc
// service is still there to be started again.

const servicesMount = "/services"

// The service named after the Containerfile's CMD.
const cmdService = "app"

const serviceLabelPrefix = "tps.service."

// cacheLabel names the directories a project keeps warm across its tasks
// (see cacheDir): absolute container paths, separated by spaces or commas.
const cacheLabel = "tps.cache"

var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)

// The last serviceLogMax bytes of a service's output go to the dashboard.
const serviceLogMax = 32 << 10

// A declaredService is one Containerfile.dev names, in its order there.
type declaredService struct {
	Name, Cmd string
}

// A serviceState is what the dashboard shows of one service.
type serviceState struct {
	Name     string `json:"name"`
	Cmd      string `json:"cmd"`
	Declared bool   `json:"declared,omitempty"`
	Status   string `json:"status"` // idle (never started) | running | exited | stopped
	Code     *int   `json:"code,omitempty"`
	Started  int64  `json:"started,omitempty"` // unix ms
	Ended    int64  `json:"ended,omitempty"`
}

var instructionRe = regexp.MustCompile(`^(?i)(CMD|FROM|LABEL)\s+(.*)$`)

// containerfileServices reads the services a Containerfile declares (see containerfileDeclarations).
func containerfileServices(text string) []declaredService {
	services, _ := containerfileDeclarations(text)
	return services
}

// containerfileDeclarations reads what a Containerfile declares to TPS, all of
// it in its final stage: the services — its CMD (the last one, as in the
// image) as 'app', and every tps.service.<name> label, in order; a label named
// app replaces the CMD's entry — and the cache directories (see cacheLabel).
func containerfileDeclarations(text string) (services []declaredService, caches []string) {
	var cmd []string
	var labels []declaredService
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\\\r\n", ""), "\\\n", "")
	for _, line := range strings.Split(text, "\n") {
		m := instructionRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		arg := strings.TrimSpace(m[2])
		switch strings.ToUpper(m[1]) {
		case "FROM":
			cmd, labels, caches = nil, nil, nil
		case "CMD":
			var parsed []string
			if strings.HasPrefix(arg, "[") && json.Unmarshal([]byte(arg), &parsed) == nil {
				cmd = parsed
			} else {
				cmd = []string{"/bin/sh", "-c", arg}
			}
		case "LABEL":
			for _, kv := range parseLabels(arg) {
				if kv[0] == cacheLabel {
					for _, path := range strings.FieldsFunc(kv[1], func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
						if filepath.IsAbs(path) {
							caches = append(caches, filepath.Clean(path))
						}
					}
					continue
				}
				name := strings.TrimPrefix(kv[0], serviceLabelPrefix)
				if name == kv[0] || !serviceNameRe.MatchString(name) || strings.TrimSpace(kv[1]) == "" {
					continue
				}
				labels = append(labels, declaredService{name, strings.TrimSpace(kv[1])})
			}
		}
	}
	if line := shellLine(cmd); line != "" {
		services = append(services, declaredService{cmdService, line})
	}
	for _, l := range labels {
		if i := indexService(services, l.Name); i >= 0 {
			services[i] = l
		} else {
			services = append(services, l)
		}
	}
	return services, caches
}

func indexService(list []declaredService, name string) int {
	for i, s := range list {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// parseLabels reads the key=value pairs of a LABEL instruction: values (and
// keys) may be double-quoted, with backslash escapes, or bare words. The old
// "LABEL key value" form has no '=' and is left alone.
func parseLabels(arg string) [][2]string {
	var pairs [][2]string
	rest := arg
	for {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return pairs
		}
		key, r, ok := labelToken(rest, "=")
		if !ok {
			return pairs
		}
		value, r2, _ := labelToken(r, "")
		pairs = append(pairs, [2]string{key, value})
		rest = r2
	}
}

// labelToken reads one token off the front of s: a double-quoted string, or
// a bare word running up to whitespace or the terminator; it returns the
// token, the remainder past the terminator, and whether the terminator (when
// one is asked for) was found.
func labelToken(s, term string) (tok, rest string, ok bool) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
				i++
			}
			i++ // the closing quote
		case c == '\\' && i+1 < len(s):
			b.WriteByte(s[i+1])
			i += 2
		case c == ' ' || c == '\t':
			return b.String(), s[i:], term == ""
		case term != "" && string(c) == term:
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), "", term == ""
}

// shellLine writes an exec-form command as a shell line: "sh -c X" is X
// itself, anything else is quoted where needed.
func shellLine(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	if len(argv) == 3 && (argv[0] == "/bin/sh" || argv[0] == "sh") && argv[1] == "-c" {
		return argv[2]
	}
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

var bareWordRe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

func shellQuote(a string) string {
	if bareWordRe.MatchString(a) {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// --- the services directory ---

func (t *Task) servicesDir() string { return filepath.Join(t.dir(), "services") }

// writeDeclared puts the declared commands where the tool finds them:
// .declared/<name> in the services dir, one line each.
func writeDeclared(dir string, declared []declaredService) {
	d := filepath.Join(dir, ".declared")
	_ = os.RemoveAll(d)
	if len(declared) == 0 {
		return
	}
	_ = os.MkdirAll(d, 0o755)
	for _, s := range declared {
		_ = os.WriteFile(filepath.Join(d, s.Name), []byte(s.Cmd+"\n"), 0o644)
	}
}

// resetService forgets a run of a service: its pid, log and exit code — all
// of the directory but the argv that says what the service is. For one about
// to run again, and for a container being replaced (see resetServices).
func resetService(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "argv" {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// resetServices forgets the runs of every service: for a container about to
// be replaced, whose processes go with it. The commands stay, declared or ad
// hoc, so every service the task has can be started again in the new one.
func resetServices(dir string) {
	_ = os.MkdirAll(dir, 0o755)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && serviceNameRe.MatchString(e.Name()) {
			resetService(filepath.Join(dir, e.Name()))
		}
	}
}

// readServices reads the state of every service from its files: the declared
// ones first, in their order, then the rest by name. A declared service that
// is not started shows the command its declaration has now, which is what
// starting it would run; one that ran shows the command it ran with.
func readServices(dir string, declared []declaredService) []serviceState {
	var out []serviceState
	seen := map[string]bool{}
	for _, d := range declared {
		s := readService(dir, d.Name)
		s.Declared = true
		if s.Status == "idle" {
			s.Cmd = d.Cmd
		}
		out = append(out, s)
		seen[d.Name] = true
	}
	entries, _ := os.ReadDir(dir)
	var rest []string
	for _, e := range entries {
		if e.IsDir() && !seen[e.Name()] && serviceNameRe.MatchString(e.Name()) {
			rest = append(rest, e.Name())
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		out = append(out, readService(dir, name))
	}
	return out
}

// serviceArgv is what a service runs, as the tool wrote it down: the
// arguments NUL-separated, a single one of them being a shell line.
func serviceArgv(dir string) []string {
	raw := readFile(filepath.Join(dir, "argv"))
	if raw == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
}

// serviceCmd is that command as one would type it.
func serviceCmd(argv []string) string {
	if len(argv) == 1 {
		return argv[0] // a shell line, shown as one
	}
	return shellLine(argv)
}

func readService(dir, name string) serviceState {
	d := filepath.Join(dir, name)
	s := serviceState{Name: name, Status: "idle", Cmd: serviceCmd(serviceArgv(d))}
	pid, err := os.Stat(filepath.Join(d, "pid"))
	if err != nil {
		return s
	}
	s.Started = pid.ModTime().UnixMilli()
	s.Status = "running"
	if exit, err := os.Stat(filepath.Join(d, "exit")); err == nil {
		code, _ := strconv.Atoi(strings.TrimSpace(readFile(filepath.Join(d, "exit"))))
		s.Code = &code
		s.Ended = exit.ModTime().UnixMilli()
		s.Status = "exited"
		if exists(filepath.Join(d, "stopped")) {
			s.Status = "stopped"
		}
	}
	return s
}

// serviceLogTail is the last serviceLogMax bytes of a service's output, cut
// at a line start.
func serviceLogTail(dir, name string) string {
	f, err := os.Open(filepath.Join(dir, name, "log"))
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	buf := make([]byte, min(st.Size(), serviceLogMax))
	if n, err := f.ReadAt(buf, st.Size()-int64(len(buf))); err != nil && err != io.EOF {
		return ""
	} else {
		buf = buf[:n]
	}
	if st.Size() > serviceLogMax {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return string(buf)
}

// --- the task's side ---

// setDeclaredL takes a freshly read Containerfile's declarations on board,
// and hands them to the tool.
func (t *Task) setDeclaredL(declared []declaredService) {
	t.declared = declared
	writeDeclared(t.servicesDir(), declared)
}

// syncDeclared re-reads Containerfile.dev, so the services on offer follow
// what was just written there. Running one is an exec in the container that
// is already up, so a new command needs no rebuild to be usable — only the
// image the file describes does, and that waits for the container to be
// recreated anyway.
func (t *Task) syncDeclared() {
	if !t.hasWorkspace() {
		return
	}
	declared := containerfileServices(t.containerfile())
	t.lock()
	defer t.unlock()
	if t.status != StatusUp || sameDeclared(declared, t.declared) {
		return
	}
	t.setDeclaredL(declared)
	t.syncServicesL(t.viewers > 0)
}

func sameDeclared(a, b []declaredService) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// servicesL is what the dashboard shows: nil while there is no container.
func (t *Task) servicesL() any {
	if t.status != StatusUp || t.container == nil || len(t.services) == 0 {
		return nil
	}
	return t.services
}

func (t *Task) anyRunningL() bool {
	for _, s := range t.services {
		if s.Status == "running" {
			return true
		}
	}
	return false
}

// syncServicesL reads the services' state off disk and publishes what
// changed; with logs, their output tails too (for a task someone is looking
// at). A service seen running that ended badly on its own gets a note in
// the chat, as the agent's or the user's run of it would deserve.
func (t *Task) syncServicesL(logs bool) {
	if t.status != StatusUp || t.container == nil {
		return
	}
	was := map[string]string{}
	for _, s := range t.services {
		was[s.Name] = s.Status
	}
	t.services = readServices(t.servicesDir(), t.declared)
	for _, s := range t.services {
		if was[s.Name] == "running" && s.Status == "exited" && s.Code != nil && *s.Code != 0 {
			t.note(fmt.Sprintf("service %s exited (%d)", s.Name, *s.Code))
		}
	}
	t.pubL("services", t.servicesL())
	if !logs {
		return
	}
	names := map[string]bool{}
	for _, s := range t.services {
		if s.Status == "idle" {
			continue
		}
		names[s.Name] = true
		t.p.m.hub.Set([]string{"projects", t.p.pid, "tasks", t.tid, "serviceLogs", s.Name}, serviceLogTail(t.servicesDir(), s.Name))
	}
	for name := range t.logsPublished {
		if !names[name] {
			t.p.m.hub.Set([]string{"projects", t.p.pid, "tasks", t.tid, "serviceLogs", name}, nil)
		}
	}
	t.logsPublished = names
}

// clearServicesL is for a container gone: nothing runs, and nothing is shown.
func (t *Task) clearServicesL() {
	t.services = nil
	t.pubL("services", nil)
	t.pubL("serviceLogs", nil)
	t.logsPublished = nil
}

// serviceTool runs the tool in the task's container, bringing that up first,
// and returns what it had to say on failure.
func (t *Task) serviceTool(args ...string) error {
	c, err := t.up()
	if err != nil {
		return err
	}
	argv := append([]string{"podman", "exec", c.Name, "bash", "-lc", `exec /tps/bin/tps-service-manager "$@"`, "tps-service-manager"}, args...)
	r, err := runCmd(argv, RunOpts{Timeout: 30 * time.Second})
	if err != nil {
		if msg := strings.TrimSpace(r.Err + "\n" + r.Out); msg != "" {
			return errors.New(strings.TrimPrefix(msg, "tps-service-manager: "))
		}
		return err
	}
	t.lock()
	if t.container == c {
		t.touchL()
		t.syncServicesL(t.viewers > 0)
	}
	t.unlock()
	return nil
}

func checkServiceName(name string) error {
	if !serviceNameRe.MatchString(name) {
		return fmt.Errorf("%q is not a service name", name)
	}
	return nil
}

// RunService starts a service by name: a declared one, or one that ran before.
func (t *Task) RunService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	return t.serviceTool("run", name)
}

func (t *Task) StopService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	return t.serviceTool("stop", name)
}

func (t *Task) RestartService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	return t.serviceTool("restart", name)
}

// syncServices is the manager's tick: every running task's services are
// re-read, and the output of those someone is looking at.
func (m *Manager) syncServices() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.allTasksL() {
		if t.status == StatusUp && t.container != nil {
			t.syncServicesL(t.viewers > 0)
		}
	}
}
