package podnester

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// Published ports are mapped the way bind mounts are: a client asking for
// -p 5432:5432 means "reachable on port 5432 in the top-level container",
// just as a bind source means a path in that container's filesystem. Podman
// itself can only publish on the host, which the top-level container cannot
// reach (and which it would be wrong to expose to). So the proxy publishes a
// port itself: a forwarder — this binary, started inside the top-level
// container through the API's exec — listens there and relays every
// connection to the sibling's address on the shared network.
//
// A forwarder is told its listen address and target on the command line and
// lives as long as its control file exists: a file in the directory the
// proxy and the top-level container share (the one holding the socket).
// Deleting the file is how the proxy stops it, which needs no signal into
// the container. A forwarder asked for port 0 picks one and writes the
// choice into the control file, which is how "publish on any port" works.

// Forward relays connections accepted on listen to target until the control
// file disappears. It is the sibling of a `podman run -p`: the proxy runs it
// in the top-level container.
func Forward(control, listen, target string) error {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		_ = os.WriteFile(control, []byte("error: "+err.Error()+"\n"), 0o644)
		return err
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	if err := os.WriteFile(control, []byte("port: "+port+"\n"), 0o644); err != nil {
		return err
	}
	go func() {
		for {
			if _, err := os.Stat(control); err != nil {
				l.Close()
				return
			}
			time.Sleep(time.Second)
		}
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if _, statErr := os.Stat(control); statErr != nil {
				return nil // told to stop
			}
			return err
		}
		go relay(conn, target)
	}
}

func relay(conn net.Conn, target string) {
	defer conn.Close()
	out, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		return
	}
	defer out.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(out, conn)
		closeWrite(out)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, out)
		closeWrite(conn)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

// The proxy's side: which forwarders exist for which container, and keeping
// that in step with the containers that run.

// forwarder is one published port of one sibling.
type forwarder struct {
	control string // host path of the control file
	target  string // ip:port on the shared network
	port    string // the port it listens on, once known
}

// wantedForwarders is what a running sibling's port label asks for: listen
// address → target, given the sibling's address on the shared network.
func wantedForwarders(pb portBindings, ip string) map[string]string {
	out := map[string]string{}
	for port, bindings := range pb {
		private, proto := splitPort(port)
		if proto != "tcp" {
			continue
		}
		for _, b := range bindings {
			host := b.HostIP
			if host == "" || host == "0.0.0.0" {
				host = ""
			}
			out[net.JoinHostPort(host, b.HostPort)] = fmt.Sprintf("%s:%d", ip, private)
		}
	}
	return out
}

// sync brings the forwarders of the owner in line with its running siblings:
// one per published port of each, none for containers that stopped. Called
// after the requests that change that, and on a timer to catch what they
// miss (a container dying on its own, a --rm one going away).
func (p *Proxy) syncForwarders(ctx context.Context) {
	p.fwdMu.Lock()
	defer p.fwdMu.Unlock()
	list, err := p.up.listContainers(ctx, p.ownerLabel())
	if err != nil {
		p.logf("listing containers for port forwarding: %v", err)
		return
	}
	wanted := map[string]map[string]string{} // container id → listen → target
	for _, c := range list {
		if c.State != "running" || c.Labels[portsLabel] == "" {
			continue
		}
		insp, err := p.up.inspectContainer(ctx, c.ID)
		if err != nil {
			continue
		}
		ip := insp.NetworkSettings.Networks[p.Network()].IPAddress
		if ip == "" {
			continue
		}
		var pb portBindings
		if err := unmarshal(c.Labels[portsLabel], &pb); err != nil {
			continue
		}
		wanted[c.ID] = wantedForwarders(pb, ip)
	}
	for id, fwds := range p.fwds {
		for listen, f := range fwds {
			if wanted[id][listen] != f.target {
				p.stopForwarder(f)
				delete(fwds, listen)
			}
		}
		if len(fwds) == 0 {
			delete(p.fwds, id)
		}
	}
	for id, w := range wanted {
		for listen, target := range w {
			if p.fwds[id] != nil && p.fwds[id][listen] != nil {
				continue
			}
			f, err := p.startForwarder(ctx, id, listen, target)
			if err != nil {
				p.logf("forwarding %s to %s: %v", listen, target, err)
				continue
			}
			if p.fwds[id] == nil {
				p.fwds[id] = map[string]*forwarder{}
			}
			p.fwds[id][listen] = f
		}
	}
}

func (p *Proxy) startForwarder(ctx context.Context, id, listen, target string) (*forwarder, error) {
	name := fmt.Sprintf("fwd-%s-%s", id[:12], strings.ReplaceAll(strings.ReplaceAll(listen, ":", "_"), "/", "_"))
	f := &forwarder{control: p.cfg.Control + "/" + name, target: target}
	if err := os.WriteFile(f.control, []byte("starting\n"), 0o644); err != nil {
		return nil, err
	}
	argv := append(append([]string{}, p.cfg.Forwarder...), "--control", p.cfg.ControlMount+"/"+name, listen, target)
	var created struct {
		ID string `json:"Id"`
	}
	if err := p.up.call(ctx, "POST", "/containers/"+p.cfg.Owner+"/exec", nil, map[string]any{
		"Cmd": argv, "AttachStdout": false, "AttachStderr": false, "Detach": true,
	}, &created); err != nil {
		os.Remove(f.control)
		return nil, err
	}
	if err := p.up.call(ctx, "POST", "/exec/"+created.ID+"/start", nil, map[string]any{"Detach": true}, nil); err != nil {
		os.Remove(f.control)
		return nil, err
	}
	// The forwarder reports its port (chosen, when asked for 0) or its trouble.
	for i := 0; i < 50; i++ {
		data, _ := os.ReadFile(f.control)
		if s := strings.TrimSpace(string(data)); strings.HasPrefix(s, "port: ") {
			f.port = strings.TrimPrefix(s, "port: ")
			return f, nil
		} else if strings.HasPrefix(s, "error: ") {
			os.Remove(f.control)
			return nil, fmt.Errorf("%s", strings.TrimPrefix(s, "error: "))
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Remove(f.control)
	return nil, fmt.Errorf("the forwarder did not report back (is %s a static binary the container can run?)", p.cfg.Forwarder[0])
}

func (p *Proxy) stopForwarder(f *forwarder) { _ = os.Remove(f.control) }

// stopForwarders removes every forwarder, including any left by an earlier
// proxy on the same control directory.
func (p *Proxy) stopForwarders() {
	p.fwdMu.Lock()
	defer p.fwdMu.Unlock()
	entries, _ := os.ReadDir(p.cfg.Control)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "fwd-") {
			_ = os.Remove(p.cfg.Control + "/" + e.Name())
		}
	}
	p.fwds = map[string]map[string]*forwarder{}
}

// actualPorts is what the forwarders of a container listen on, by the
// container port they serve: the answer to "which port did I get?".
func (p *Proxy) actualPorts(id string, pb portBindings) map[string]string {
	p.fwdMu.Lock()
	defer p.fwdMu.Unlock()
	out := map[string]string{}
	for port, bindings := range pb {
		for _, b := range bindings {
			host := b.HostIP
			if host == "0.0.0.0" {
				host = ""
			}
			if f := p.fwds[id][net.JoinHostPort(host, b.HostPort)]; f != nil {
				out[port] = f.port
			}
		}
	}
	return out
}
