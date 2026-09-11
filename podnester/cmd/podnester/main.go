// podnester is podman, except that `podnester run` gives the container a
// Docker socket of its own, backed by the host's podman and held to what
// the container may see. Every other command is podman's.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vanviegen/podnester"
)

func main() {
	if handled, err := podnester.Subcommand(os.Args); handled {
		fail(err)
	}
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
	}
	switch args[0] {
	case "run":
		fail(run(args[1:]))
	case "serve":
		fail(serve(args[1:]))
	case "purge":
		fail(purge(args[1:]))
	case "help", "--help", "-h":
		usage()
	default: // podman's own
		fail(syscall.Exec(podmanPath(), append([]string{"podman"}, args...), os.Environ()))
	}
}

func fail(err error) {
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "podnester:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `podnester is podman, except that its run gives the container docker.

  podnester run [podman run options] IMAGE [COMMAND...]
      As podman run, plus a docker socket inside (DOCKER_HOST is set): what
      the container starts through it is its own, on a network of its own,
      and removed when the container exits. --name and --security-opt are read
      off the options and applied to what it starts as well; what it starts
      shares its user namespace. Without --name, one is made up. With -d, podnester keeps
      serving the socket in the background for as long as the container exists.
  podnester serve NAME [--security-opt OPT]...
      Serve the socket for a container run some other way (see the README).
  podnester purge [--volumes] NAME
      Remove what a container made: its sub-containers and networks, and
      with --volumes its volumes.
  podnester COMMAND...
      Anything else is podman's.

The socket is served from $XDG_RUNTIME_DIR/podnester/NAME (or $PODNESTER_DIR/NAME),
against podman's API on $XDG_RUNTIME_DIR/podman/podman.sock, started if not running.
`)
	os.Exit(2)
}

func podmanPath() string {
	p, err := exec.LookPath("podman")
	if err != nil {
		fail(err)
	}
	return p
}

func runtimeDir() string {
	if d := os.Getenv("PODNESTER_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "podnester")
	}
	return filepath.Join(os.TempDir(), "podnester")
}

// config is a proxy for a container, with podman's API made sure of.
func config(name string, securityOpt []string) (podnester.Config, error) {
	sock := filepath.Join(filepath.Dir(runtimeDir()), "podman", "podman.sock")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		sock = filepath.Join(runtimeDir(), "podman.sock")
	}
	if _, err := podnester.EnsureService(sock); err != nil {
		return podnester.Config{}, err
	}
	return podnester.Config{
		Upstream: sock, Owner: name, SecurityOpt: securityOpt,
		Control: filepath.Join(runtimeDir(), name), ControlMount: "/run/podnester",
	}, nil
}

// run is podman run, with the container's name and security options read
// off the arguments, and the socket added.
func run(args []string) error {
	name, securityOpt, detach := scanRun(args)
	if name == "" {
		name = fmt.Sprintf("podnester-%d", time.Now().UnixNano()%1000000)
		args = append([]string{"--name", name}, args...)
	}
	cfg, err := config(name, securityOpt)
	if err != nil {
		return err
	}
	p, err := podnester.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := p.EnsureNetwork(ctx); err != nil {
		return err
	}
	args = append([]string{"run", "--network", p.Network(),
		"-v", cfg.Control + ":" + cfg.ControlMount,
		"-e", "DOCKER_HOST=unix://" + p.SocketMount(),
		"-e", "CONTAINER_HOST=unix://" + p.SocketMount(),
		"-e", "DOCKER_BUILDKIT=0"}, args...)
	if detach {
		// The socket outlives this command: a serve of our own, in the background.
		self, err := os.Executable()
		if err != nil {
			return err
		}
		logFile, _ := os.OpenFile(filepath.Join(cfg.Control, "serve.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		bg := exec.Command(self, append([]string{"serve", name}, optArgs("--security-opt", securityOpt)...)...)
		bg.Stdout, bg.Stderr = logFile, logFile
		bg.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := bg.Start(); err != nil {
			return err
		}
		cmd := exec.Command("podman", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	go func() {
		if err := p.ListenAndServe(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "podnester:", err)
		}
	}()
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	if err := p.RemoveContainers(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "podnester:", err)
	}
	return runErr
}

// scanRun reads the container's name, security options and detachment off
// podman run arguments.
func scanRun(args []string) (name string, securityOpt []string, detach bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break // the image
		}
		opt, val, has := strings.Cut(a, "=")
		next := func() string {
			if has {
				return val
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch opt {
		case "--name":
			name = next()
		case "--security-opt":
			securityOpt = append(securityOpt, next())
		case "--userns":
			next() // podman's to read; skipped so its value is not taken for the image
		case "-d", "--detach":
			detach = !has || val == "true"
		default:
			// A bundle of short flags (-dit) may hold d. Flags with values
			// bundle their value instead (-eFOO), which holds no d as a flag.
			if !strings.HasPrefix(a, "--") && len(a) > 2 && strings.Trim(a, "-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" &&
				!strings.ContainsAny(a[1:2], "evplhmwu") && strings.Contains(a, "d") {
				detach = true
			}
		}
	}
	return name, securityOpt, detach
}

func optArgs(opt string, values []string) []string {
	var out []string
	for _, v := range values {
		out = append(out, opt, v)
	}
	return out
}

// serve serves a container's socket until the container is gone.
func serve(args []string) error {
	name, rest := positional(args)
	var securityOpt []string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--security-opt":
			i++
			securityOpt = append(securityOpt, rest[i])
		default:
			usage()
		}
	}
	cfg, err := config(name, securityOpt)
	if err != nil {
		return err
	}
	p, err := podnester.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := p.EnsureNetwork(ctx); err != nil {
		return err
	}
	go func() {
		// Until the container is gone: give it a minute to appear first.
		time.Sleep(time.Minute)
		for {
			if exists, err := p.OwnerExists(ctx); err == nil && !exists {
				cancel()
				return
			}
			time.Sleep(15 * time.Second)
		}
	}()
	return p.ListenAndServe(ctx)
}

func purge(args []string) error {
	name, rest := positional(args)
	volumes := len(rest) == 1 && rest[0] == "--volumes"
	if len(rest) > 1 || len(rest) == 1 && !volumes {
		usage()
	}
	cfg, err := config(name, nil)
	if err != nil {
		return err
	}
	cfg.Forwarder = []string{"-"} // not needed for this
	p, err := podnester.New(cfg)
	if err != nil {
		return err
	}
	return p.Purge(context.Background(), volumes)
}

// positional takes the one non-option argument (the name) out of args.
func positional(args []string) (string, []string) {
	var rest []string
	name := ""
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") || name != "" {
			rest = append(rest, args[i])
		} else {
			name = args[i]
		}
	}
	if name == "" {
		usage()
	}
	return name, rest
}
