// TPS: a kanban-style manager for AI coding agents working in podman dev
// containers. One binary, two roles: the UI (default) serves the web app and
// relays to daemons; --daemon runs the workflow on a host and keeps running
// without a UI.
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/vanviegen/agent-manager/daemon"
	"github.com/vanviegen/agent-manager/ui"
)

//go:embed web/index.html web/dist
var webFS embed.FS

func main() {
	log.SetFlags(log.Ltime)
	isDaemon := flag.Bool("daemon", false, "run as the daemon (the UI starts one when needed)")
	port := flag.Int("port", 4820, "port for the web UI")
	flag.IntVar(port, "p", 4820, "port for the web UI (shorthand)")
	host := flag.String("host", "127.0.0.1", "address to listen on")
	noOpen := flag.Bool("no-open", false, "don't open the browser")
	daemonBinary := flag.String("daemon-binary", "", "binary to install on remote hosts of another architecture")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: tps [--port 4820] [--host 127.0.0.1] [--no-open] [--daemon-binary FILE] | tps --daemon")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *isDaemon {
		if err := daemon.Serve(ui.BuildID()); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Fatal(ui.Run(ui.Options{Addr: fmt.Sprintf("%s:%d", *host, *port), WebFS: webFS, Open: !*noOpen, DaemonBinary: *daemonBinary}))
}
