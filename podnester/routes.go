package podnester

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// The endpoints of the Docker API the proxy knows, and what it does with
// each. Anything else is refused: the answer says so, naming the endpoint.

type route struct {
	methods string // "GET,HEAD"
	pattern string // "/containers/{id}/json"; "{name...}" spans several segments
	handle  func(c *call) error
}

var routes = []route{
	{"GET,HEAD", "/_ping", passThrough},
	{"GET,HEAD", "/libpod/_ping", passThrough}, // the podman CLI's first request; the rest of libpod gets a pointer to docker
	{"GET", "/version", passThrough},
	{"GET", "/info", passThrough},
	{"GET", "/system/df", passThrough},
	{"GET", "/events", filtered(true)},

	{"GET", "/containers/json", filtered(false)},
	{"POST", "/containers/create", handleCreate},
	{"POST", "/containers/prune", filtered(false)},
	{"GET", "/containers/{id}/json", containerInspectRoute},
	{"GET", "/containers/{id}/top", ownContainerRoute(false)},
	{"GET", "/containers/{id}/changes", ownContainerRoute(false)},
	{"GET", "/containers/{id}/logs", ownContainerRoute(true)},
	{"GET", "/containers/{id}/stats", ownContainerRoute(true)},
	{"GET", "/containers/{id}/export", ownContainerRoute(true)},
	{"GET,HEAD,PUT", "/containers/{id}/archive", ownContainerRoute(true)},
	{"POST", "/containers/{id}/start", ownContainerRoute(false, syncPorts)},
	{"POST", "/containers/{id}/restart", ownContainerRoute(false, syncPorts)},
	{"POST", "/containers/{id}/unpause", ownContainerRoute(false, syncPorts)},
	{"POST", "/containers/{id}/stop", ownContainerRoute(false, syncPorts)},
	{"POST", "/containers/{id}/kill", ownContainerRoute(false, syncPorts)},
	{"POST", "/containers/{id}/pause", ownContainerRoute(false)},
	{"POST", "/containers/{id}/wait", ownContainerRoute(true, syncPorts)}, // a long poll: the answer must not be held back for rewriting
	{"POST", "/containers/{id}/resize", ownContainerRoute(false)},
	{"POST", "/containers/{id}/attach", ownContainerRoute(true)},
	{"POST", "/containers/{id}/rename", renameRoute},
	{"POST", "/containers/{id}/update", handleUpdate},
	{"POST", "/containers/{id}/exec", handleExecCreate},
	{"DELETE", "/containers/{id}", ownContainerRoute(false, syncPorts)},

	{"POST", "/exec/{id}/start", execRoute(true)},
	{"POST", "/exec/{id}/resize", execRoute(false)},
	{"GET", "/exec/{id}/json", execRoute(false)},

	{"GET", "/images/json", passThrough},
	{"GET", "/images/search", passThrough},
	{"GET", "/images/get", streamThrough},
	{"POST", "/images/create", streamThrough},
	{"POST", "/images/load", streamThrough},
	{"POST", "/build", buildRoute},
	{"GET", "/images/{name...}/json", passThrough},
	{"GET", "/images/{name...}/history", passThrough},
	{"GET", "/images/{name...}/get", streamThrough},
	{"POST", "/images/{name...}/tag", passThrough},
	{"GET", "/distribution/{name...}/json", passThrough},
	{"POST", "/commit", commitRoute},

	{"GET", "/volumes", filtered(false)},
	{"POST", "/volumes/create", handleVolumeCreate},
	{"POST", "/volumes/prune", filtered(false)},
	{"GET", "/volumes/{name}", ownVolumeRoute},
	{"DELETE", "/volumes/{name}", ownVolumeRoute},

	{"GET", "/networks", filtered(false)},
	{"POST", "/networks/create", handleNetworkCreate},
	{"POST", "/networks/prune", handleNetworkPrune},
	{"GET", "/networks/{id}", ownNetworkRoute},
	{"DELETE", "/networks/{id}", networkDeleteRoute},
	{"POST", "/networks/{id}/connect", networkConnectRoute},
	{"POST", "/networks/{id}/disconnect", networkConnectRoute},
}

func matchRoute(method, path string) (*route, map[string]string) {
	for i := range routes {
		r := &routes[i]
		if !contains(strings.Split(r.methods, ","), method) {
			continue
		}
		if params, ok := matchPattern(r.pattern, path); ok {
			return r, params
		}
	}
	return nil, nil
}

func matchPattern(pattern, path string) (map[string]string, bool) {
	pp, pa := split(pattern), split(path)
	params := map[string]string{}
	i := 0
	for j, seg := range pp {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "...}") {
			n := len(pa) - i - (len(pp) - j - 1)
			if n < 1 {
				return nil, false
			}
			params[seg[1:len(seg)-4]] = strings.Join(pa[i:i+n], "/")
			i += n
			continue
		}
		if i >= len(pa) {
			return nil, false
		}
		if strings.HasPrefix(seg, "{") {
			params[seg[1:len(seg)-1]] = pa[i]
		} else if seg != pa[i] {
			return nil, false
		}
		i++
	}
	return params, i == len(pa)
}

func passThrough(c *call) error { return c.pass() }

func streamThrough(c *call) error {
	c.stream = true
	return c.pass()
}

// filtered narrows a listing (or a prune) to the owner's resources.
func filtered(stream bool) func(c *call) error {
	return func(c *call) error {
		if err := withLabelFilter(c.query, c.p.ownerLabel(), c.p.cfg.Prefix); err != nil {
			return badRequest("filters: %v", err)
		}
		c.stream = stream
		return c.pass()
	}
}

// ownContainerRoute passes a request on a container of the owner's, with
// the path rewritten to its id; after is run once podman has answered.
func ownContainerRoute(stream bool, after ...func(c *call)) func(c *call) error {
	return func(c *call) error {
		insp, err := c.ownContainer(c.params["id"])
		if err != nil {
			return err
		}
		c.path = strings.Replace(c.path, "/"+c.params["id"], "/"+insp.ID, 1)
		c.stream = stream
		if len(after) > 0 {
			c.after = func() {
				for _, f := range after {
					f(c)
				}
			}
		}
		return c.pass()
	}
}

func syncPorts(c *call) { c.p.syncForwarders(c.ctx) }

// containerInspectRoute is GET /containers/{id}/json, with the published
// ports the proxy serves filled back in.
func containerInspectRoute(c *call) error {
	insp, err := c.ownContainer(c.params["id"])
	if err != nil {
		return err
	}
	c.path = "/containers/" + insp.ID + "/json"
	var pb portBindings
	_ = unmarshal(insp.Config.Labels[portsLabel], &pb)
	actual := c.p.actualPorts(insp.ID, pb)
	c.mutate = func(v any) any {
		m, ok := v.(map[string]any)
		if !ok || len(pb) == 0 {
			return v
		}
		if hc, ok := m["HostConfig"].(map[string]any); ok {
			hc["PortBindings"] = inspectPorts(pb, actual)
		}
		if ns, ok := m["NetworkSettings"].(map[string]any); ok {
			ns["Ports"] = inspectPorts(pb, actual)
		}
		return m
	}
	return c.pass()
}

func renameRoute(c *call) error {
	insp, err := c.ownContainer(c.params["id"])
	if err != nil {
		return err
	}
	c.path = "/containers/" + insp.ID + "/rename"
	c.query.Set("name", prefixed(c.query.Get("name"), c.p.cfg.Prefix))
	return c.pass()
}

// execRoute checks an exec id belongs to a container of the owner's.
func execRoute(stream bool) func(c *call) error {
	return func(c *call) error {
		var e execInspect
		if err := c.p.up.call(c.ctx, "GET", "/exec/"+url.PathEscape(c.params["id"])+"/json", nil, nil, &e); err != nil {
			if isNotFound(err) {
				return &httpError{http.StatusNotFound, "No such exec instance: " + c.params["id"]}
			}
			return err
		}
		if _, err := c.ownContainer(e.ContainerID); err != nil {
			return &httpError{http.StatusNotFound, "No such exec instance: " + c.params["id"]}
		}
		c.stream = stream
		return c.pass()
	}
}

// buildRoute is POST /build: the context comes along as a tar in the body,
// so nothing of the host is named; the query is held to known parameters.
func buildRoute(c *call) error {
	for key, vals := range c.query {
		switch key {
		case "dockerfile", "t", "extrahosts", "remote", "q", "nocache", "cachefrom", "pull", "rm", "forcerm",
			"memory", "memswap", "cpushares", "cpusetcpus", "cpuperiod", "cpuquota", "buildargs", "shmsize",
			"squash", "labels", "platform", "target", "version", "layers", "httpproxy", "cpusetmems", "ulimits", "buildid":
		case "networkmode":
			for _, v := range vals {
				if v != "" && v != "default" && v != "bridge" && v != "none" {
					return denied("build networkmode %q is not allowed", v)
				}
			}
		case "cgroupparent": // sent empty by the docker CLI on every build; a value would put the build in a cgroup of the client's choosing
			for _, v := range vals {
				if v != "" {
					return denied("build cgroupparent %q is not allowed", v)
				}
			}
			delete(c.query, key)
		default:
			return denied("build parameter %q is not allowed through this socket", key)
		}
	}
	c.stream = true
	return c.pass()
}

// commitRoute is POST /commit?container=…: an image from a sibling.
func commitRoute(c *call) error {
	insp, err := c.ownContainer(c.query.Get("container"))
	if err != nil {
		return err
	}
	c.query.Set("container", insp.ID)
	return c.pass()
}

func ownVolumeRoute(c *call) error {
	v, err := c.ownVolume(c.params["name"])
	if err != nil {
		return err
	}
	c.path = "/volumes/" + url.PathEscape(v.Name)
	return c.pass()
}

func ownNetworkRoute(c *call) error {
	n, err := c.ownNetwork(c.params["id"])
	if err != nil {
		return err
	}
	c.path = "/networks/" + url.PathEscape(n.Name)
	return c.pass()
}

// networkDeleteRoute takes the owner off the network first, as podman
// keeps a network that has containers on it.
func networkDeleteRoute(c *call) error {
	n, err := c.ownNetwork(c.params["id"])
	if err != nil {
		return err
	}
	if n.Name == c.p.Network() {
		return denied("the default network cannot be removed")
	}
	_ = c.p.up.call(c.ctx, "POST", "/networks/"+url.PathEscape(n.Name)+"/disconnect", nil, map[string]any{"Container": c.p.cfg.Owner, "Force": true}, nil)
	c.path = "/networks/" + url.PathEscape(n.Name)
	return c.pass()
}

// networkConnectRoute is connect and disconnect: an owner's network, and an
// owner's container in the body.
func networkConnectRoute(c *call) error {
	n, err := c.ownNetwork(c.params["id"])
	if err != nil {
		return err
	}
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	insp, err := c.ownContainer(str(body["Container"]))
	if err != nil {
		return err
	}
	body["Container"] = marshal(insp.ID)
	for key := range body {
		switch key {
		case "Container", "EndpointConfig", "Force":
		default:
			return denied("%s is not a field this socket accepts here", key)
		}
	}
	c.writeJSON(body)
	c.path = "/networks/" + url.PathEscape(n.Name) + strings.TrimPrefix(c.path, "/networks/"+c.params["id"])
	c.after = func() { c.p.syncForwarders(c.ctx) }
	return c.pass()
}

// A listing of containers carries their Ports; those the proxy serves are
// filled in from the label.
func init() {
	for i := range routes {
		if routes[i].pattern == "/containers/json" {
			routes[i].handle = containerListRoute
		}
	}
}

func containerListRoute(c *call) error {
	if err := withLabelFilter(c.query, c.p.ownerLabel(), c.p.cfg.Prefix); err != nil {
		return badRequest("filters: %v", err)
	}
	c.mutate = func(v any) any {
		list, ok := v.([]any)
		if !ok {
			return v
		}
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			labels, _ := m["Labels"].(map[string]any)
			raw, _ := labels[portsLabel].(string)
			if raw == "" {
				continue
			}
			var pb portBindings
			if json.Unmarshal([]byte(raw), &pb) != nil {
				continue
			}
			id, _ := m["Id"].(string)
			m["Ports"] = summaryPorts(pb, c.p.actualPorts(id, pb))
		}
		return list
	}
	return c.pass()
}

func split(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}
