// podnester runs a container with a Docker-compatible socket of its own,
// backed by the host's podman and held to what that container may see.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/vanviegen/podnester"
)

func main() {
	if handled, err := podnester.Subcommand(os.Args); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "serve":
		err = serve(os.Args[2:])
	case "purge":
		err = purge(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage:
  podnester run --name NAME [options] -- [podman run arguments...]
      Run a container with its own docker socket, in the foreground; its
      sub-containers are removed when it exits. --name, --network and the
      socket mount are added to the podman run arguments given.
  podnester serve --name NAME [options]
      Serve the socket for a container someone else runs (started with
      --network NAME-bridge and the control directory mounted).
  podnester purge --name NAME [--volumes]
      Remove what a container made: its sub-containers and networks, and
      with --volumes its volumes.
Options:
  --userns MODE       user namespace mode forced on sub-containers, e.g.
                      keep-id:uid=1000,gid=1000 (give the top-level container the same)
  --security-opt OPT  added to every sub-container (repeatable), e.g. label=disable
  --control DIR       host directory shared with the container (default
                      $XDG_RUNTIME_DIR/podnester/NAME)
  --mount PATH        where it is mounted in the container (default /run/podnester)
  --upstream SOCKET   podman's API socket (default $XDG_RUNTIME_DIR/podman/podman.sock;
                      a podman system service is started on it when nothing answers)
`)
	os.Exit(2)
}

type options struct {
	cfg     podnester.Config
	volumes bool
}

func parse(args []string, purgeFlags bool) (options, []string) {
	var o options
	fs := flag.NewFlagSet("podnester", flag.ExitOnError)
	fs.Usage = usage
	fs.StringVar(&o.cfg.Owner, "name", "", "")
	fs.StringVar(&o.cfg.UsernsMode, "userns", "", "")
	fs.Func("security-opt", "", func(s string) error { o.cfg.SecurityOpt = append(o.cfg.SecurityOpt, s); return nil })
	fs.StringVar(&o.cfg.Control, "control", "", "")
	fs.StringVar(&o.cfg.ControlMount, "mount", "/run/podnester", "")
	fs.StringVar(&o.cfg.Upstream, "upstream", "", "")
	if purgeFlags {
		fs.BoolVar(&o.volumes, "volumes", false, "")
	}
	_ = fs.Parse(args)
	if o.cfg.Owner == "" {
		usage()
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	if o.cfg.Control == "" {
		o.cfg.Control = filepath.Join(runtimeDir, "podnester", o.cfg.Owner)
	}
	if o.cfg.Upstream == "" {
		o.cfg.Upstream = filepath.Join(runtimeDir, "podman", "podman.sock")
	}
	return o, fs.Args()
}

func newProxy(o options) (*podnester.Proxy, error) {
	if _, err := podnester.EnsureService(o.cfg.Upstream); err != nil {
		return nil, err
	}
	return podnester.New(o.cfg)
}

func run(args []string) error {
	o, rest := parse(args, false)
	p, err := newProxy(o)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := p.EnsureNetwork(ctx); err != nil {
		return err
	}
	go func() {
		if err := p.ListenAndServe(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	argv := append([]string{"run", "--name", o.cfg.Owner, "--network", p.Network(),
		"-v", o.cfg.Control + ":" + o.cfg.ControlMount,
		"-e", "DOCKER_HOST=unix://" + p.SocketMount(),
		"-e", "CONTAINER_HOST=unix://" + p.SocketMount(),
		"-e", "DOCKER_BUILDKIT=0"}, rest...)
	cmd := exec.CommandContext(ctx, "podman", argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	if err := p.RemoveContainers(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return runErr
}

func serve(args []string) error {
	o, _ := parse(args, false)
	p, err := newProxy(o)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := p.EnsureNetwork(ctx); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "serving %s for %s (network %s)\n", p.Socket(), o.cfg.Owner, p.Network())
	return p.ListenAndServe(ctx)
}

func purge(args []string) error {
	o, _ := parse(args, true)
	p, err := newProxy(o)
	if err != nil {
		return err
	}
	return p.Purge(context.Background(), o.volumes)
}
