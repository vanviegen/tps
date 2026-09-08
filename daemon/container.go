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
	"regexp"
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
		return os.WriteFile(settingsFile, []byte("{\n\t\"workbench.colorTheme\": \"Default Dark Modern\"\n}\n"), 0o644)
	}
	// Add a dark theme to seeded settings that don't pick one. Settings with
	// comments (JSONC) don't parse; leave those untouched.
	var settings map[string]any
	if data, err := os.ReadFile(settingsFile); err == nil && json.Unmarshal(data, &settings) == nil {
		if _, ok := settings["workbench.colorTheme"]; !ok {
			settings["workbench.colorTheme"] = "Default Dark Modern"
			if out, err := json.MarshalIndent(settings, "", "\t"); err == nil {
				_ = os.WriteFile(settingsFile, append(out, '\n'), 0o644)
			}
		}
	}
	return nil
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
# with uid 1000 who owns a home directory. Anything the task serves should
# listen on $PORT.

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

const (
	codePort = 9000 // code-server inside the container
	appPort  = 8080 // $PORT, for whatever the task itself serves
)

type Container struct {
	Name     string
	CodePort int // published on the host loopback
	AppPort  int
}

type containerOpts struct {
	name, image, toolbox, repoDir, claudeDir string
}

// ensureContainer makes sure a container by this name, based on this image
// and toolbox, is running with the task's repo clone mounted at /work and its
// claude state dir at /claude. Reuses a running match; otherwise replaces.
func ensureContainer(o containerOpts) (*Container, error) {
	config := containerConfig(o.image, o.toolbox)
	if c := runningContainer(o.name, config); c != nil {
		return c, nil
	}
	rmContainer(o.name)
	vscode, err := sharedVscodeDir()
	if err != nil {
		return nil, err
	}
	args := []string{
		"run", "-d", "--name", o.name, "--label", "tps.config=" + config,
		"--userns=keep-id:uid=1000,gid=1000", "--user", "1000:1000",
		// SELinux separation is off so the mounts stay usable without
		// relabeling the user's real files.
		"--security-opt", "label=disable",
		"-v", o.toolbox + ":/tps:ro",
		"-v", o.repoDir + ":/work",
		"-v", o.claudeDir + ":/claude",
		"-v", vscode + ":/vscode",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", "DISABLE_AUTOUPDATER=1", // the toolbox is read-only, and versioned by TPS
		"-e", fmt.Sprintf("PORT=%d", appPort),
		"-p", fmt.Sprintf("127.0.0.1::%d", codePort),
		"-p", fmt.Sprintf("127.0.0.1::%d", appPort),
		"-w", "/work",
	}
	if creds := filepath.Join(home(), ".claude", ".credentials.json"); exists(creds) {
		args = append(args, "-v", creds+":/tps-host-claude-credentials.json:ro")
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		args = append(args, "-e", "ANTHROPIC_API_KEY")
	}
	// code-server under the toolbox's init, with the toolbox on PATH so
	// terminals in VS Code can run claude too. The explicit --port keeps
	// code-server from picking up $PORT, which is meant for whatever the task
	// itself serves.
	args = append(args, o.image, "/tps/bin/tini", "--", "sh", "-c", fmt.Sprintf(
		"export PATH=/tps/bin:$PATH; exec code-server --bind-addr 0.0.0.0:%[1]d --port %[1]d --auth none --disable-workspace-trust"+
			" --user-data-dir /vscode --extensions-dir /vscode/extensions /work", codePort))
	if _, err := runCmd(append([]string{"podman"}, args...), RunOpts{}); err != nil {
		return nil, err
	}
	code, app := containerPorts(o.name)
	if code == 0 || app == 0 {
		return nil, fmt.Errorf("container %s has no published ports", o.name)
	}
	c := &Container{o.name, code, app}
	return c, c.waitReady()
}

// containerConfig is the label value recording what a container was started
// with. Bump the version when ensureContainer's run command/args change, so
// existing containers are recycled instead of reused.
func containerConfig(image, toolbox string) string {
	config, _ := json.Marshal([]any{7, image, filepath.Base(toolbox)})
	return string(config)
}

// runningContainer finds a running container of ours by name, started with
// exactly this config (per the label ensureContainer sets).
func runningContainer(name, config string) *Container {
	inspect, _ := runCmd([]string{"podman", "inspect", "--format", `{{index .Config.Labels "tps.config"}}` + "\t" + `{{.State.Running}}`, name}, RunOpts{NoCheck: true})
	if strings.TrimSpace(inspect.Out) != config+"\ttrue" {
		return nil
	}
	code, app := containerPorts(name)
	if code == 0 || app == 0 {
		return nil
	}
	return &Container{name, code, app}
}

func rmContainer(name string) {
	_, _ = runCmd([]string{"podman", "rm", "-f", "-t", "2", name}, RunOpts{NoCheck: true})
}

var portRe = regexp.MustCompile(`:(\d+)\s*$`)

func containerPorts(name string) (code, app int) {
	get := func(cport int) int {
		r, _ := runCmd([]string{"podman", "port", name, fmt.Sprintf("%d/tcp", cport)}, RunOpts{NoCheck: true})
		if m := portRe.FindStringSubmatch(strings.TrimSpace(r.Out)); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		return 0
	}
	return get(codePort), get(appPort)
}

func (c *Container) waitReady() error {
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < 120; i++ {
		if resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", c.CodePort)); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("code-server in %s did not come up", c.Name)
}

// Exec runs a bash script in the container and waits for it.
func (c *Container) Exec(script string) error {
	return exec.CommandContext(context.Background(), "podman", "exec", c.Name, "bash", "-lc", script).Run()
}

func (c *Container) Rm() { rmContainer(c.Name) }
