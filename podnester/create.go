package podnester

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// A container spec is where a client could ask for the host: a bind mount
// of /, a device, a capability, the host's namespaces. Every field of the
// spec is known here, as passed through, refused unless empty, or checked
// and rewritten; a field that is not known is refused too, so an addition
// to the API that the proxy has not seen cannot slip past it.

// createCtx carries what one create request gathers on the way.
type createCtx struct {
	c        *call
	table    *mountTable
	warnings []string
	ports    portBindings
}

func (cc *createCtx) warn(format string, args ...any) {
	cc.warnings = append(cc.warnings, "podnester: "+fmt.Sprintf(format, args...))
}

// mounts is the owner's filesystem view, inspected once per request.
func (cc *createCtx) mounts() (*mountTable, error) {
	if cc.table == nil {
		t, err := cc.c.ownerTable()
		if err != nil {
			return nil, err
		}
		cc.table = t
	}
	return cc.table, nil
}

// hostPath is the host path a bind source (a path in the owner) means.
func (cc *createCtx) hostPath(src string) (string, error) {
	t, err := cc.mounts()
	if err != nil {
		return "", err
	}
	hpath, err := t.resolveVia(cc.c.p.cfg.Resolver, src)
	if err != nil {
		return "", denied("bind mount %s: %v", src, err)
	}
	return hpath, nil
}

// handleCreate is POST /containers/create.
func handleCreate(c *call) error {
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	cc := &createCtx{c: c}
	out, err := cc.container(body)
	if err != nil {
		return err
	}
	c.writeJSON(out)
	// A name of the client's gets the prefix; one podman would make up would not, so make one up here.
	name := c.query.Get("name")
	if name == "" {
		name = randomName()
	}
	c.query.Set("name", prefixed(name, c.p.cfg.Prefix))
	warnings := cc.warnings
	c.mutate = func(v any) any {
		if m, ok := v.(map[string]any); ok && len(warnings) > 0 {
			var list []any
			if w, ok := m["Warnings"].([]any); ok {
				list = w
			}
			for _, w := range warnings {
				list = append(list, w)
			}
			m["Warnings"] = list
		}
		return v
	}
	return c.pass()
}

// container checks and rewrites a create body.
func (cc *createCtx) container(body map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for key, v := range body {
		switch key {
		case "Hostname", "Domainname", "User", "AttachStdin", "AttachStdout", "AttachStderr", "ExposedPorts",
			"Tty", "OpenStdin", "StdinOnce", "Env", "Cmd", "Healthcheck", "ArgsEscaped", "Image", "Volumes",
			"WorkingDir", "Entrypoint", "NetworkDisabled", "MacAddress", "OnBuild", "StopSignal", "StopTimeout", "Shell":
			out[key] = v
		case "Labels":
			labels := map[string]string{}
			if err := unmarshalNonNull(v, &labels); err != nil {
				return nil, badRequest("Labels: %v", err)
			}
			out[key] = cc.labels(labels)
		case "HostConfig":
			hc := map[string]json.RawMessage{}
			if err := unmarshalNonNull(v, &hc); err != nil {
				return nil, badRequest("HostConfig: %v", err)
			}
			rewritten, err := cc.hostConfig(hc)
			if err != nil {
				return nil, err
			}
			out[key] = marshal(rewritten)
		case "NetworkingConfig":
			nc := struct {
				EndpointsConfig map[string]json.RawMessage
			}{}
			if err := unmarshalNonNull(v, &nc); err != nil {
				return nil, badRequest("NetworkingConfig: %v", err)
			}
			endpoints := map[string]json.RawMessage{}
			for name, settings := range nc.EndpointsConfig {
				n, err := cc.c.ownNetwork(name)
				if err != nil {
					return nil, err
				}
				endpoints[n.Name] = settings
			}
			out[key] = marshal(map[string]any{"EndpointsConfig": endpoints})
		default:
			return nil, denied("%s is not a field this socket accepts in a container spec", key)
		}
	}
	if _, ok := out["Labels"]; !ok {
		out["Labels"] = cc.labels(map[string]string{})
	}
	if _, ok := out["HostConfig"]; !ok {
		hc, err := cc.hostConfig(map[string]json.RawMessage{})
		if err != nil {
			return nil, err
		}
		out["HostConfig"] = marshal(hc)
	}
	return out, nil
}

func (cc *createCtx) labels(labels map[string]string) json.RawMessage {
	p := cc.c.p
	labels[p.cfg.Label] = p.cfg.Owner
	if len(cc.ports) > 0 {
		labels[portsLabel] = string(marshal(cc.ports))
	} else {
		delete(labels, portsLabel)
	}
	return marshal(labels)
}

// hostConfig checks and rewrites HostConfig. Labels are written after it,
// as the ports it records go there.
func (cc *createCtx) hostConfig(hc map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	p := cc.c.p
	out := map[string]json.RawMessage{}
	for key, v := range hc {
		var err error
		switch key {
		case "LogConfig":
			var lc struct {
				Type   string
				Config map[string]string
			}
			if err := unmarshalNonNull(v, &lc); err != nil {
				return nil, badRequest("LogConfig: %v", err)
			}
			if len(lc.Config) > 0 {
				return nil, denied("LogConfig.Config is not allowed (log paths and drivers are the host's)")
			}
			out[key] = v
		case "RestartPolicy", "AutoRemove", "ConsoleSize", "CapDrop", "Dns", "DnsOptions", "DnsSearch", "ExtraHosts",
			"GroupAdd", "OomScoreAdj", "ReadonlyRootfs", "Tmpfs", "ShmSize", "Isolation", "Init",
			"CpuShares", "Memory", "NanoCpus", "BlkioWeight", "CpuPeriod", "CpuQuota", "CpusetCpus", "CpusetMems",
			"MemoryReservation", "MemorySwap", "MemorySwappiness", "OomKillDisable", "PidsLimit", "Ulimits",
			"CpuCount", "CpuPercent", "IOMaximumIOps", "IOMaximumBandwidth", "KernelMemory", "KernelMemoryTCP":
			out[key] = v
		case "ContainerIDFile", "Annotations", "Cgroup", "Links", "StorageOpt", "Sysctls", "Runtime", "CgroupParent",
			"Devices", "DeviceCgroupRules", "DeviceRequests", "MaskedPaths", "ReadonlyPaths", "CapAdd",
			"BlkioWeightDevice", "BlkioDeviceReadBps", "BlkioDeviceWriteBps", "BlkioDeviceReadIOps", "BlkioDeviceWriteIOps",
			"CpuRealtimePeriod", "CpuRealtimeRuntime":
			if !isEmpty(v) {
				return nil, denied("HostConfig.%s is not allowed through this socket", key)
			}
			out[key] = v
		case "Privileged", "PublishAllPorts":
			if !isEmpty(v) {
				return nil, denied("HostConfig.%s is not allowed through this socket", key)
			}
			out[key] = v
		case "VolumeDriver":
			var s string
			_ = json.Unmarshal(v, &s)
			if s != "" && s != "local" {
				return nil, denied("HostConfig.VolumeDriver %q is not allowed", s)
			}
			out[key] = v
		case "CgroupnsMode", "UTSMode":
			if s := str(v); s != "" && s != "private" {
				return nil, denied("HostConfig.%s %q is not allowed", key, s)
			}
			out[key] = v
		case "PidMode", "IpcMode":
			out[key], err = cc.namespaceMode(key, str(v))
		case "NetworkMode":
			out[key], err = cc.networkMode(str(v))
		case "UsernsMode":
			s := str(v)
			if s != p.cfg.UsernsMode {
				if s != "" {
					cc.warn("UsernsMode %q ignored: siblings run with the same user mapping as this container", s)
				}
				s = p.cfg.UsernsMode
			}
			out[key] = marshal(s)
		case "SecurityOpt":
			var opts []string
			if err := unmarshalNonNull(v, &opts); err != nil {
				return nil, badRequest("SecurityOpt: %v", err)
			}
			out[key], err = cc.securityOpt(opts)
		case "Binds":
			var binds []string
			if err := unmarshalNonNull(v, &binds); err != nil {
				return nil, badRequest("Binds: %v", err)
			}
			for i, b := range binds {
				if binds[i], err = cc.bind(b); err != nil {
					return nil, err
				}
			}
			out[key] = marshal(binds)
		case "Mounts":
			var mounts []map[string]json.RawMessage
			if err := unmarshalNonNull(v, &mounts); err != nil {
				return nil, badRequest("Mounts: %v", err)
			}
			for i, m := range mounts {
				if mounts[i], err = cc.mount(m); err != nil {
					return nil, err
				}
			}
			out[key] = marshal(mounts)
		case "VolumesFrom":
			var refs []string
			if err := unmarshalNonNull(v, &refs); err != nil {
				return nil, badRequest("VolumesFrom: %v", err)
			}
			for i, ref := range refs {
				name, mode, _ := strings.Cut(ref, ":")
				insp, err := cc.c.ownContainer(name)
				if err != nil {
					return nil, err
				}
				refs[i] = insp.ID
				if mode != "" {
					refs[i] += ":" + mode
				}
			}
			out[key] = marshal(refs)
		case "PortBindings":
			var pb portBindings
			if err := unmarshalNonNull(v, &pb); err != nil {
				return nil, badRequest("PortBindings: %v", err)
			}
			if err := cc.portBindings(pb); err != nil {
				return nil, err
			}
			out[key] = marshal(map[string]any{}) // the proxy publishes them, not podman
		default:
			return nil, denied("HostConfig.%s is not a field this socket accepts", key)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, ok := hc["NetworkMode"]; !ok {
		out["NetworkMode"] = marshal(p.Network())
	}
	if _, ok := hc["UsernsMode"]; !ok && p.cfg.UsernsMode != "" {
		out["UsernsMode"] = marshal(p.cfg.UsernsMode)
	}
	if _, ok := hc["SecurityOpt"]; !ok && len(p.cfg.SecurityOpt) > 0 {
		out["SecurityOpt"] = marshal(p.cfg.SecurityOpt)
	}
	return out, nil
}

// namespaceMode checks a pid or ipc mode: the container's own, or a sibling's.
func (cc *createCtx) namespaceMode(key, mode string) (json.RawMessage, error) {
	switch {
	case mode == "" || mode == "private" || mode == "shareable" || mode == "none":
		return marshal(mode), nil
	case strings.HasPrefix(mode, "container:"):
		insp, err := cc.c.ownContainer(strings.TrimPrefix(mode, "container:"))
		if err != nil {
			return nil, err
		}
		return marshal("container:" + insp.ID), nil
	}
	return nil, denied("HostConfig.%s %q is not allowed", key, mode)
}

// networkMode maps the client's network onto the owner's: its default is
// the owner's network, "none" is fine, a sibling's namespace is fine, a
// network of its own must be one of the owner's, and the host's is not on.
func (cc *createCtx) networkMode(mode string) (json.RawMessage, error) {
	switch {
	case mode == "" || mode == "default" || mode == "bridge":
		return marshal(cc.c.p.Network()), nil
	case mode == "none":
		return marshal(mode), nil
	case mode == "host":
		return nil, denied("HostConfig.NetworkMode \"host\" is not allowed")
	case strings.HasPrefix(mode, "container:"):
		insp, err := cc.c.ownContainer(strings.TrimPrefix(mode, "container:"))
		if err != nil {
			return nil, err
		}
		return marshal("container:" + insp.ID), nil
	}
	n, err := cc.c.ownNetwork(mode)
	if err != nil {
		return nil, err
	}
	return marshal(n.Name), nil
}

func (cc *createCtx) securityOpt(opts []string) (json.RawMessage, error) {
	var out []string
	for _, o := range opts {
		switch {
		case o == "no-new-privileges", strings.HasPrefix(o, "no-new-privileges:"), o == "label=disable":
			out = append(out, o)
		default:
			return nil, denied("HostConfig.SecurityOpt %q is not allowed", o)
		}
	}
	for _, o := range cc.c.p.cfg.SecurityOpt {
		if !contains(out, o) {
			out = append(out, o)
		}
	}
	return marshal(out), nil
}

// bind rewrites one entry of Binds: "src:dst[:opts]", with src a path in the
// owner or a volume name of the client's.
func (cc *createCtx) bind(spec string) (string, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return spec, nil // just a container path: an anonymous volume
	}
	src, dst := parts[0], parts[1]
	var opts []string
	if len(parts) > 2 {
		opts = strings.Split(parts[2], ",")
		for _, o := range opts {
			if err := checkMountOpt(o); err != nil {
				return "", err
			}
		}
	}
	var err error
	if strings.HasPrefix(src, "/") {
		src, err = cc.hostPath(src)
	} else {
		src, err = cc.c.ensureVolume(src)
	}
	if err != nil {
		return "", err
	}
	out := src + ":" + dst
	if len(opts) > 0 {
		out += ":" + strings.Join(opts, ",")
	}
	return out, nil
}

func checkMountOpt(o string) error {
	switch o {
	case "", "ro", "rw", "nocopy", "private", "rprivate", "exec", "noexec", "suid", "nosuid", "dev", "nodev":
		return nil
	case "z", "Z":
		return denied("mount option %q is not allowed: it would relabel the host's files (SELinux labels are off for siblings, so it is not needed)", o)
	}
	return denied("mount option %q is not allowed", o)
}

// mount rewrites one entry of Mounts (the --mount form).
func (cc *createCtx) mount(m map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	typ := str(m["Type"])
	for key, v := range m {
		switch key {
		case "Type", "Target", "ReadOnly", "Consistency", "TmpfsOptions":
			out[key] = v
		case "Source":
			src := str(v)
			var err error
			switch {
			case typ == "bind":
				src, err = cc.hostPath(src)
			case typ == "volume" && src != "":
				src, err = cc.c.ensureVolume(src)
			case typ == "tmpfs" || src == "":
			default:
				err = denied("mount type %q is not allowed", typ)
			}
			if err != nil {
				return nil, err
			}
			out[key] = marshal(src)
		case "BindOptions":
			var bo struct {
				Propagation string
			}
			if err := unmarshalNonNull(v, &bo); err != nil {
				return nil, badRequest("BindOptions: %v", err)
			}
			if bo.Propagation != "" && bo.Propagation != "private" && bo.Propagation != "rprivate" {
				return nil, denied("mount propagation %q is not allowed", bo.Propagation)
			}
			out[key] = v
		case "VolumeOptions":
			var vo struct {
				DriverConfig *struct {
					Name    string
					Options map[string]string
				}
			}
			if err := unmarshalNonNull(v, &vo); err != nil {
				return nil, badRequest("VolumeOptions: %v", err)
			}
			if vo.DriverConfig != nil && (vo.DriverConfig.Name != "" && vo.DriverConfig.Name != "local" || len(vo.DriverConfig.Options) > 0) {
				return nil, denied("volume driver options are not allowed")
			}
			out[key] = v
		default:
			if !isEmpty(v) {
				return nil, denied("mount option %s is not allowed", key)
			}
		}
	}
	switch typ {
	case "bind", "volume", "tmpfs":
	default:
		return nil, denied("mount type %q is not allowed", typ)
	}
	if typ == "bind" {
		if _, ok := m["Source"]; !ok {
			return nil, badRequest("a bind mount needs a Source")
		}
	}
	return out, nil
}

// portBindings checks what the client wants published. Podman is not asked
// to publish anything; the bindings go into a label and the proxy's
// forwarders serve them in the owner.
func (cc *createCtx) portBindings(pb portBindings) error {
	if len(pb) == 0 {
		return nil
	}
	cc.ports = portBindings{}
	for port, bindings := range pb {
		_, proto := splitPort(port)
		if proto != "tcp" {
			return denied("publishing %s: only tcp ports can be published through this socket", port)
		}
		var list []portBinding
		for _, b := range bindings {
			switch b.HostIP {
			case "", "0.0.0.0", "127.0.0.1", "localhost":
			default:
				return denied("publishing %s on %s: ports are published inside this container, on 0.0.0.0 or 127.0.0.1", port, b.HostIP)
			}
			if b.HostIP == "localhost" {
				b.HostIP = "127.0.0.1"
			}
			if b.HostPort == "" {
				b.HostPort = "0"
			}
			if _, err := fmt.Sscanf(b.HostPort, "%d", new(int)); err != nil || atoi(b.HostPort) > 65535 {
				return badRequest("publishing %s: bad host port %q (ranges are not supported)", port, b.HostPort)
			}
			list = append(list, b)
		}
		cc.ports[port] = list
	}
	return nil
}

// handleUpdate is POST /containers/{id}/update: resources and the restart
// policy, checked like the HostConfig they belong to.
func handleUpdate(c *call) error {
	insp, err := c.ownContainer(c.params["id"])
	if err != nil {
		return err
	}
	c.params["id"] = insp.ID
	c.path = "/containers/" + insp.ID + "/update"
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	cc := &createCtx{c: c}
	for key := range body {
		switch key {
		case "Binds", "Mounts", "NetworkMode", "PortBindings", "UsernsMode", "SecurityOpt", "VolumesFrom", "PidMode", "IpcMode", "LogConfig":
			return denied("%s cannot be updated", key)
		}
	}
	out, err := cc.hostConfig(body)
	if err != nil {
		return err
	}
	delete(out, "NetworkMode")
	delete(out, "UsernsMode")
	delete(out, "SecurityOpt")
	c.writeJSON(out)
	return c.pass()
}

// handleExecCreate is POST /containers/{id}/exec.
func handleExecCreate(c *call) error {
	insp, err := c.ownContainer(c.params["id"])
	if err != nil {
		return err
	}
	c.path = "/containers/" + insp.ID + "/exec"
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	for key, v := range body {
		switch key {
		case "AttachStdin", "AttachStdout", "AttachStderr", "ConsoleSize", "DetachKeys", "Tty", "Env", "Cmd", "User", "WorkingDir", "Detach":
		case "Privileged":
			if !isEmpty(v) {
				return denied("a privileged exec is not allowed")
			}
		default:
			return denied("%s is not a field this socket accepts in an exec", key)
		}
	}
	c.writeJSON(body)
	return c.pass()
}

// handleVolumeCreate is POST /volumes/create.
func handleVolumeCreate(c *call) error {
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	labels := map[string]string{}
	for key, v := range body {
		switch key {
		case "Labels":
			if err := unmarshalNonNull(v, &labels); err != nil {
				return badRequest("Labels: %v", err)
			}
		case "Name":
			body[key] = marshal(prefixed(str(v), c.p.cfg.Prefix))
		case "Driver":
			if s := str(v); s != "" && s != "local" {
				return denied("volume driver %q is not allowed", s)
			}
		case "DriverOpts", "ClusterVolumeSpec":
			if !isEmpty(v) {
				return denied("%s is not allowed: volume options can name host paths", key)
			}
		default:
			return denied("%s is not a field this socket accepts for a volume", key)
		}
	}
	labels[c.p.cfg.Label] = c.p.cfg.Owner
	body["Labels"] = marshal(labels)
	c.writeJSON(body)
	return c.pass()
}

// handleNetworkCreate is POST /networks/create. The owner joins every
// network made through it, so that it can reach the containers on it by
// name, as compose expects.
func handleNetworkCreate(c *call) error {
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	labels := map[string]string{}
	name := ""
	for key, v := range body {
		switch key {
		case "Labels":
			if err := unmarshalNonNull(v, &labels); err != nil {
				return badRequest("Labels: %v", err)
			}
		case "Name":
			name = prefixed(str(v), c.p.cfg.Prefix)
			body[key] = marshal(name)
		case "CheckDuplicate", "Internal", "Attachable", "EnableIPv6", "Scope":
		case "Driver":
			if s := str(v); s != "" && s != "bridge" {
				return denied("network driver %q is not allowed", s)
			}
		case "IPAM":
			var ipam struct {
				Driver  string
				Config  []any
				Options map[string]string
			}
			if err := unmarshalNonNull(v, &ipam); err != nil {
				return badRequest("IPAM: %v", err)
			}
			if ipam.Driver != "" && ipam.Driver != "default" || len(ipam.Config) > 0 || len(ipam.Options) > 0 {
				return denied("IPAM settings are not allowed: subnets are the host's to pick")
			}
		case "Options", "Ingress", "ConfigOnly", "ConfigFrom":
			if !isEmpty(v) {
				return denied("network %s is not allowed", key)
			}
		default:
			return denied("%s is not a field this socket accepts for a network", key)
		}
	}
	if name == "" {
		return badRequest("a network needs a Name")
	}
	labels[c.p.cfg.Label] = c.p.cfg.Owner
	body["Labels"] = marshal(labels)
	c.writeJSON(body)
	c.after = func() {
		if err := c.p.up.call(c.ctx, "POST", "/networks/"+name+"/connect", nil, map[string]any{"Container": c.p.cfg.Owner}, nil); err != nil && !isNotFound(err) {
			c.p.logf("joining %s to network %s: %v", c.p.cfg.Owner, name, err)
		}
	}
	return c.pass()
}

// handleNetworkPrune is POST /networks/prune, done here: podman would keep
// every network the owner is on, which is all of them.
func handleNetworkPrune(c *call) error {
	var nets []networkInspect
	if err := c.p.up.call(c.ctx, "GET", "/networks", map[string][]string{"filters": {labelFilter(c.p.ownerLabel())}}, nil, &nets); err != nil {
		return err
	}
	var deleted []string
	for _, n := range nets {
		if n.Name == c.p.Network() {
			continue
		}
		full, err := c.p.up.inspectNetwork(c.ctx, n.Name)
		if err != nil {
			continue
		}
		busy := false
		for _, v := range full.Containers {
			if m, ok := v.(map[string]any); ok && m["Name"] != c.p.cfg.Owner {
				busy = true
			}
		}
		if busy {
			continue
		}
		if err := c.p.removeNetwork(c.ctx, n.Name); err == nil {
			deleted = append(deleted, strings.TrimPrefix(n.Name, c.p.cfg.Prefix))
		}
	}
	sort.Strings(deleted)
	c.w.Header().Set("Content-Type", "application/json")
	c.w.WriteHeader(http.StatusOK)
	return json.NewEncoder(c.w).Encode(map[string]any{"NetworksDeleted": deleted})
}

// JSON helpers.

func marshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func str(v json.RawMessage) string {
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

func unmarshalNonNull(v json.RawMessage, out any) error {
	if isEmpty(v) {
		return nil
	}
	return json.Unmarshal(v, out)
}

// isEmpty says a JSON value carries nothing: null, false, 0, "", [], {}.
func isEmpty(v json.RawMessage) bool {
	switch strings.TrimSpace(string(v)) {
	case "", "null", "false", "0", `""`, "[]", "{}":
		return true
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// randomName is a name for a container created without one.
func randomName() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("c%x", b)
}
