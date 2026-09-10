// TPS: a kanban-style manager for AI coding agents working in podman dev
// containers. One binary, two roles: the UI (default) serves the web app and
// relays to daemons; --daemon runs the workflow on a host and keeps running
// without a UI.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"github.com/vanviegen/agent-manager/daemon"
	"github.com/vanviegen/agent-manager/sshx"
	"github.com/vanviegen/agent-manager/ui"
	"github.com/vanviegen/podnester"
)

//go:embed web/index.html web/dist
var webFS embed.FS

func main() {
	log.SetFlags(log.Ltime)
	// The podnester helpers: run inside task containers (port forwarding) and under podman unshare (path resolving).
	if handled, err := podnester.Subcommand(os.Args); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	// Invoked by ssh as its askpass program: `tps "<prompt>"`, with our socket in the environment.
	if sock := os.Getenv("TPS_ASKPASS_SOCK"); sock != "" && len(os.Args) == 2 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(ui.Askpass(sock, os.Getenv("TPS_ASKPASS_HOST"), os.Getenv("TPS_ASKPASS_RUN"), os.Args[1]))
	}
	isDaemon := flag.Bool("daemon", false, "run as the daemon (the UI starts one when needed)")
	port := flag.Int("port", 4820, "port for the web UI")
	flag.IntVar(port, "p", 4820, "port for the web UI (shorthand)")
	host := flag.String("host", "127.0.0.1", "address to listen on")
	noOpen := flag.Bool("no-open", false, "don't open the browser")
	daemonBinary := flag.String("daemon-binary", "", "binary to install on remote hosts of another architecture")
	relayPath := flag.String("relay", "", "internal: relay stdio to the unix socket at this path")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: tps [--port 4820] [--host 127.0.0.1] [--no-open] [--daemon-binary FILE] | tps --daemon")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *relayPath != "" {
		relay(*relayPath)
		return
	}
	if *isDaemon {
		if err := daemon.Serve(ui.BuildID()); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Fatal(ui.Run(ui.Options{Addr: fmt.Sprintf("%s:%d", *host, *port), WebFS: webFS, Open: !*noOpen, DaemonBinary: *daemonBinary}))
}

// relay connects stdio to a unix socket; the UI runs it over ssh to reach a
// remote daemon.
func relay(path string) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Stdout.WriteString(sshx.RelayOK)
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.(*net.UnixConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
}
