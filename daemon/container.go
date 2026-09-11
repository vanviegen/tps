package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VS Code state (settings, keybindings, snippets, extensions, UI state) lives
// in one shared directory, mounted into every task container and used by the
// code-server on a project's own checkout (see code.go). Configure once, and
// every task of every project has it; it also survives container rebuilds. Seeded from the host's code-server or desktop VS Code settings on
// first use, defaulting to a dark theme. Trade-off of sharing the whole data
// dir: workspace UI state is keyed by folder path (/work everywhere), so open
// tabs can bleed between tasks.
var (
	sharedVscodeOnce sync.Once
	sharedVscodeErr  error
)

func sharedVscodeDir() (string, error) {
	dir := filepath.Join(home(), ".local", "share", "tps", "code-server")
	sharedVscodeOnce.Do(func() { sharedVscodeErr = seedVscodeDir(dir) })
	return dir, sharedVscodeErr
}

func seedVscodeDir(dir string) error {
	userDir := filepath.Join(dir, "User")
	if err := os.MkdirAll(filepath.Join(dir, "extensions"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		return err
	}
	settingsFile := filepath.Join(userDir, "settings.json")
	if !exists(settingsFile) {
		for _, src := range []string{
			filepath.Join(home(), ".local", "share", "code-server", "User"),
			filepath.Join(home(), ".config", "Code", "User"),
		} {
			if !exists(filepath.Join(src, "settings.json")) {
				continue
			}
			for _, name := range []string{"settings.json", "keybindings.json"} {
				if data, err := os.ReadFile(filepath.Join(src, name)); err == nil {
					_ = os.WriteFile(filepath.Join(userDir, name), data, 0o644)
				}
			}
			if exists(filepath.Join(src, "snippets")) {
				_ = os.CopyFS(filepath.Join(userDir, "snippets"), os.DirFS(filepath.Join(src, "snippets")))
			}
			break
		}
	}
	if !exists(settingsFile) {
		if err := os.WriteFile(settingsFile, []byte("{}\n"), 0o644); err != nil {
			return err
		}
	}
	// Settings with comments (JSONC) don't parse; those are left untouched.
	var settings map[string]any
	if data, err := os.ReadFile(settingsFile); err == nil && json.Unmarshal(data, &settings) == nil {
		changed := false
		for k, v := range vscodeDefaults {
			if _, ok := settings[k]; !ok {
				settings[k], changed = v, true
			}
		}
		if out, err := json.MarshalIndent(settings, "", "\t"); changed && err == nil {
			_ = os.WriteFile(settingsFile, append(out, '\n'), 0o644)
		}
	}
	return nil
}

// vscodeDefaults go into settings that don't set them: a dark theme, and no
// port-forward popups (the ports the Containerfile exposes are what TPS forwards).
var vscodeDefaults = map[string]any{
	"workbench.colorTheme":    "Default Dark Modern",
	"remote.autoForwardPorts": false,
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Podman wrapper. Images are tagged by content hash of their Containerfile, so
// identical Containerfiles (across tasks and projects) share one image and a
// change triggers a rebuild exactly when needed. Builds get the task's repo
// clone as their only context, so they cannot pull in files from elsewhere.

// The image tasks run in when the repository has no Containerfile.dev of its
// own. Also placed in the toolbox (at /tps/Containerfile.dev) so an agent that
// needs more can start from it.
const defaultContainerfile = `# Dev container image for this project (Containerfile.dev).
#
# TPS builds it with the task's repo clone as the (only) build context, and
# runs the task in it as uid 1000 with that clone mounted at /work. code-server
# and claude are mounted in at run time, so nothing here is TPS-specific: use
# whatever base suits the project, as long as it has bash and git, and a user
# with uid 1000 who owns a home directory. A CMD line that starts what the
# project serves gives the dashboard a play button that runs it (as the service
# 'app'; a LABEL tps.service.<name>="command" declares another, say a test
# suite or a review app), and every port an EXPOSE line names is forwarded to
# the dashboard's machine. Listen on 0.0.0.0 there: the forwarded port arrives
# on the container's own address, so localhost-only would be unreachable, and
# TPS publishes it on the host's loopback, so 0.0.0.0 here is not exposure.

FROM docker.io/library/debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git sudo bash procps psmisc ripgrep less nano \
      openssh-client unzip zip xz-utils build-essential pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/dev
USER dev
WORKDIR /work
`

func imageTag(containerfile string) string {
	return "localhost/tps:" + sha256hex(containerfile)[:12]
}

func imageExists(tag string) bool {
	r, err := runCmd([]string{"podman", "image", "exists", tag}, RunOpts{NoCheck: true})
	return err == nil && r.Code == 0
}

type writerFunc func(string)

func (w writerFunc) Write(p []byte) (int, error) { w(string(p)); return len(p), nil }

func buildImage(tag, containerfile, contextDir string, onLog func(string)) error {
	// Written to a temp file: writing it into the context dir would dirty the worktree.
	dir, err := os.MkdirTemp("", "tps-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(file, []byte(containerfile), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("podman", "build", "-t", tag, "-f", file, contextDir)
	var tail string // the end of the build output, for the error message
	log := writerFunc(func(s string) {
		onLog(s)
		tail += s
		if len(tail) > 3000 {
			tail = tail[len(tail)-3000:]
		}
	})
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("podman build failed (%d):\n%s", ee.ExitCode(), strings.TrimSpace(tail))
		}
		return err
	}
	return nil
}

const codePort = 9000 // code-server inside the container

type Container struct {
	Name     string
	CodePort int       // code-server's port, published on the host loopback
	Ports    []PortMap // the ports the image exposes, each published likewise
}

// PortMap is one published port: Port inside the container, Host on the
// host's loopback.
type PortMap struct {
	Port, Host int
}

// exposedPorts lists the tcp ports the image declares (EXPOSE lines, its base
// images' included), in ascending order. The code-server port is left out:
// it is published regardless, and once is all podman allows.
func exposedPorts(image string) ([]int, error) {
	r, err := runCmd([]string{"podman", "image", "inspect", "--format", "{{json .Config.ExposedPorts}}", image}, RunOpts{})
	if err != nil {
		return nil, err
	}
	var exposed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.Out)), &exposed); err != nil {
		return nil, fmt.Errorf("reading the exposed ports of %s: %w", image, err)
	}
	var ports []int
	for spec := range exposed {
		if port, proto := splitPortSpec(spec); proto == "tcp" && port != codePort {
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	return ports, nil
}

// splitPortSpec reads "8080/tcp" (or "8080", which is tcp too).
func splitPortSpec(spec string) (port int, proto string) {
	num, proto, ok := strings.Cut(spec, "/")
	if !ok {
		proto = "tcp"
	}
	port, _ = strconv.Atoi(num)
	return port, strings.ToLower(proto)
}

type containerOpts struct {
	name, image, toolbox, repoDir, claudeDir string
	servicesDir                              string // the task's services (see services.go), mounted at /services
	nestDir                                  string // the task's control directory for its docker socket, see nest.go
}

// ensureContainer makes sure a container by this name, based on this image
// and toolbox, is running with the task's repo clone mounted at /work and its
// claude state dir at /claude. Reuses a running match; otherwise replaces.
func ensureContainer(o containerOpts) (*Container, error) {
	// The socket for sub-containers is served before the container starts, and a container found running gets it too.
	nest, err := nestFor(o.name, o.nestDir)
	if err != nil {
		return nil, err
	}
	config := containerConfig(o.image, o.toolbox)
	if c := runningContainer(o.name, config); c != nil {
		return c, nil
	}
	rmContainer(o.name)
	clearServices(o.servicesDir) // whatever ran in the old one is gone
	vscode, err := sharedVscodeDir()
	if err != nil {
		return nil, err
	}
	exposed, err := exposedPorts(o.image)
	if err != nil {
		return nil, err
	}
	if err := nest.EnsureNetwork(context.Background()); err != nil {
		return nil, fmt.Errorf("creating the task's network: %w", err)
	}
	args := []string{
		"run", "-d", "--name", o.name, "--label", "tps.config=" + config,
		"--userns=keep-id:uid=1000,gid=1000", "--user", "1000:1000",
		// SELinux separation is off so the mounts stay usable without
		// relabeling the user's real files.
		"--security-opt", "label=disable",
		// The task's own network, which its sub-containers join (see nest.go), and the socket they are made through.
		"--network", nest.Network(),
		"-v", o.nestDir + ":" + nestMount,
		"-e", "DOCKER_HOST=unix://" + nest.SocketMount(),
		"-e", "CONTAINER_HOST=unix://" + nest.SocketMount(),
		"-e", "DOCKER_BUILDKIT=0", // podman builds without buildkit
		"-v", o.toolbox + ":/tps:ro",
		"-v", o.repoDir + ":/work",
		"-v", o.claudeDir + ":/claude",
		"-v", o.servicesDir + ":" + servicesMount,
		"-v", vscode + ":/vscode",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", "DISABLE_AUTOUPDATER=1", // the toolbox is read-only, and versioned by TPS
		"-w", "/work",
	}
	// Every port the image exposes goes on a loopback port of the host's
	// choosing, like code-server's: nothing of a task is reachable beyond the
	// machine, and the dashboard tunnels what it shows.
	for _, port := range append([]int{codePort}, exposed...) {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1::%d", port))
	}
	// The host's claude login is shared with every task, read-write. claude
	// refreshes the OAuth tokens in that file in place, and a refresh revokes
	// the old refresh token, so private copies would log each other out.
	if creds := filepath.Join(home(), ".claude", ".credentials.json"); exists(creds) {
		// The mountpoint, made by us so it is ours; this also blanks any copy from before the file was shared.
		_ = os.WriteFile(filepath.Join(o.claudeDir, ".credentials.json"), nil, 0o600)
		args = append(args, "-v", creds+":/claude/.credentials.json")
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		args = append(args, "-e", "ANTHROPIC_API_KEY")
	}
	// The container idles under the toolbox's init: claude, the project's
	// CMD and code-server are all exec'd into it, so each comes and goes on
	// its own — VS Code in particular is started when a dashboard holds the
	// task open and stopped when none does (see StartCode), which must not
	// take the agent down with it.
	args = append(args, o.image, "/tps/bin/tini", "--", "sh", "-c", "while :; do sleep 3600; done")
	if _, err := runCmd(append([]string{"podman"}, args...), RunOpts{}); err != nil {
		return nil, err
	}
	// podman binds the ports as the container starts. A docker socket served
	// by podnester (TPS running inside a TPS task) picks them a moment later,
	// so a container short of its ports is given a few seconds to get them.
	var c *Container
	for i := 0; i < 60; i++ {
		if c = publishedContainer(o.name); c != nil && len(c.Ports) == len(exposed) {
			return c, c.waitReady()
		}
		time.Sleep(250 * time.Millisecond)
	}
	if c == nil {
		return nil, fmt.Errorf("container %s has no published ports", o.name)
	}
	return c, c.waitReady()
}

// containerConfig is the label value recording what a container was started
// with. Bump the version when ensureContainer's run command/args change, so
// existing containers are recycled instead of reused.
func containerConfig(image, toolbox string) string {
	config, _ := json.Marshal([]any{12, image, filepath.Base(toolbox)})
	return string(config)
}

// runningContainer finds a running container of ours by name, started with
// exactly this config (per the label ensureContainer sets).
func runningContainer(name, config string) *Container {
	inspect, _ := runCmd([]string{"podman", "inspect", "--format", `{{index .Config.Labels "tps.config"}}` + "\t" + `{{.State.Running}}`, name}, RunOpts{NoCheck: true})
	if strings.TrimSpace(inspect.Out) != config+"\ttrue" {
		return nil
	}
	return publishedContainer(name)
}

func rmContainer(name string) {
	_, _ = runCmd([]string{"podman", "rm", "-f", "-t", "2", name}, RunOpts{NoCheck: true})
}

// publishedContainer describes the container by the ports podman gave it;
// nil when it has none for code-server, which means it isn't one of ours.
func publishedContainer(name string) *Container {
	r, err := runCmd([]string{"podman", "inspect", "--format", "{{json .NetworkSettings.Ports}}", name}, RunOpts{NoCheck: true})
	if err != nil || r.Code != 0 {
		return nil
	}
	c := &Container{Name: name}
	for _, m := range parsePublishedPorts(r.Out) {
		if m.Port == codePort {
			c.CodePort = m.Host
		} else {
			c.Ports = append(c.Ports, m)
		}
	}
	if c.CodePort == 0 {
		return nil
	}
	return c
}

// parsePublishedPorts reads podman's port bindings, as `podman inspect` shows
// them: {"8080/tcp": [{"HostIp": "127.0.0.1", "HostPort": "45123"}]}. The
// result is sorted by container port.
func parsePublishedPorts(text string) []PortMap {
	var bindings map[string][]struct {
		HostPort string
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(text)), &bindings)
	var ports []PortMap
	for spec, list := range bindings {
		port, proto := splitPortSpec(spec)
		if proto != "tcp" || len(list) == 0 {
			continue
		}
		if host, _ := strconv.Atoi(list[0].HostPort); host != 0 {
			ports = append(ports, PortMap{port, host})
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	return ports
}

func (c *Container) waitReady() error {
	for i := 0; i < 40; i++ {
		if r, err := runCmd([]string{"podman", "exec", c.Name, "true"}, RunOpts{NoCheck: true}); err == nil && r.Code == 0 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("container %s did not come up", c.Name)
}

// codeScript runs code-server in the container, with the toolbox on PATH so
// terminals in VS Code can run claude too. The explicit --port keeps it from
// picking up a $PORT the image may set for whatever the task itself serves.
// Its pid is written down first (exec keeps it), for killCodeScript.
var codeScript = fmt.Sprintf("export PATH=/tps/bin:$PATH; echo $$ >%[2]s; exec code-server --bind-addr 0.0.0.0:%[1]d --port %[1]d"+
	" --auth none --disable-workspace-trust --user-data-dir /vscode --extensions-dir /vscode/extensions /work", codePort, codePidFile)

const codePidFile = "/tmp/.tps-code.pid"

// killCodeScript sends a signal to code-server and everything it forked
// (extension hosts, file watchers, language servers): the process tree under
// the pid codeScript wrote down, found through the parent ids in /proc. Only
// that tree — the agent's claude, and the shells it and the user run, are
// left alone whatever their command lines say. Nothing beyond /proc and the
// shell is needed of the image.
const killCodeScript = `pid=$(cat %[1]s 2>/dev/null) || exit 0
[ -d "/proc/$pid" ] || exit 0
tree=$pid; todo=$pid
while [ -n "$todo" ]; do
	next=
	for parent in $todo; do
		for p in /proc/[0-9]*; do
			s=$(cat "$p/stat" 2>/dev/null) || continue
			s=${s##*) }; set -- $s
			[ "$2" = "$parent" ] && { tree="$tree ${p#/proc/}"; next="$next ${p#/proc/}"; }
		done
	done
	todo=$next
done
kill -%[2]s $tree 2>/dev/null`

// StartCode brings code-server up in the container and waits for it to
// answer; one that is running already is left as it is.
func (c *Container) StartCode() error {
	if codeAlive(c.CodePort) {
		return nil
	}
	if _, err := runCmd([]string{"podman", "exec", "-d", c.Name, "sh", "-c", codeScript}, RunOpts{}); err != nil {
		return err
	}
	for i := 0; i < 120; i++ {
		if codeAlive(c.CodePort) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("code-server in %s did not come up", c.Name)
}

// StopCode ends code-server in the container: a TERM, and a KILL for what is
// still there a few seconds later. The container itself stays up.
func (c *Container) StopCode() {
	for _, sig := range []string{"TERM", "KILL"} {
		_, _ = runCmd([]string{"podman", "exec", c.Name, "sh", "-c", fmt.Sprintf(killCodeScript, codePidFile, sig)}, RunOpts{NoCheck: true, Timeout: 10 * time.Second})
		for i := 0; i < 12; i++ {
			if !codeAlive(c.CodePort) {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
}

// codeAlive: does a code-server answer on this (host) port?
func codeAlive(port int) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// Exec runs a bash script in the container and waits for it.
func (c *Container) Exec(script string) error {
	return exec.CommandContext(context.Background(), "podman", "exec", c.Name, "bash", "-lc", script).Run()
}

func (c *Container) Rm() {
	nestDown(c.Name)
	rmContainer(c.Name)
}
