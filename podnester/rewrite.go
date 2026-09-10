package podnester

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Names of containers, volumes and networks carry the prefix on the host and
// lose it again in every answer, so clients see the names they gave. The
// prefix is stripped wherever a string in a JSON answer starts with it (keys
// too: NetworkSettings.Networks is keyed by network name), which covers every
// place podman reports a name without a list of them to maintain.

func stripPrefix(v any, prefix string) any {
	switch x := v.(type) {
	case string:
		return strip(x, prefix)
	case []any:
		for i := range x {
			x[i] = stripPrefix(x[i], prefix)
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[strip(k, prefix)] = stripPrefix(val, prefix)
		}
		return out
	}
	return v
}

func strip(s, prefix string) string {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	if strings.HasPrefix(s, "/"+prefix) { // names in listings: "/name"
		return "/" + s[len(prefix)+1:]
	}
	return s
}

// filters is the `filters` query parameter of list endpoints: JSON, in the
// map-of-lists form ({"label":["a=b"]}) or the older map-of-maps form
// ({"label":{"a=b":true}}). Both parse into the former.
func parseFilters(raw string) (map[string][]string, error) {
	out := map[string][]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	var lists map[string][]string
	if err := json.Unmarshal([]byte(raw), &lists); err == nil {
		return lists, nil
	}
	var maps map[string]map[string]bool
	if err := json.Unmarshal([]byte(raw), &maps); err != nil {
		return nil, err
	}
	for k, m := range maps {
		for v, on := range m {
			if on {
				out[k] = append(out[k], v)
			}
		}
	}
	return out, nil
}

// withLabelFilter narrows a list query to resources carrying label, on top
// of whatever the client asked for. Name filters get the prefix.
func withLabelFilter(q url.Values, label, prefix string) error {
	f, err := parseFilters(q.Get("filters"))
	if err != nil {
		return err
	}
	f["label"] = append(f["label"], label)
	for _, key := range []string{"name", "volume", "network", "container"} {
		for i, v := range f[key] {
			f[key][i] = prefixed(v, prefix)
		}
	}
	data, _ := json.Marshal(f)
	q.Set("filters", string(data))
	return nil
}

// prefixed gives a client's name for something the host-side name: ids
// (hex) are left alone, as is a name that carries the prefix already.
func prefixed(name, prefix string) string {
	if name == "" || isHexID(name) || strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

func isHexID(s string) bool {
	if len(s) < 12 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Published ports are not podman's: the proxy records what was asked for in
// a label and serves it with forwarders (see forward.go). Inspect and list
// answers get the bindings filled back in from that label, so `docker port`,
// compose and friends see what they expect.

// portBindings is docker's HostConfig.PortBindings: "5432/tcp" → bindings.
type portBindings map[string][]portBinding

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// summaryPorts is the Ports list of a container in a listing.
func summaryPorts(pb portBindings, actual map[string]string) []any {
	var out []any
	for port, bindings := range pb {
		private, proto := splitPort(port)
		for _, b := range bindings {
			hp := b.HostPort
			if a, ok := actual[port]; ok {
				hp = a
			}
			ip := b.HostIP
			if ip == "" {
				ip = "0.0.0.0"
			}
			out = append(out, map[string]any{"IP": ip, "PrivatePort": private, "PublicPort": atoi(hp), "Type": proto})
		}
	}
	return out
}

// inspectPorts is NetworkSettings.Ports of an inspect answer.
func inspectPorts(pb portBindings, actual map[string]string) map[string]any {
	out := map[string]any{}
	for port, bindings := range pb {
		var list []any
		for _, b := range bindings {
			hp := b.HostPort
			if a, ok := actual[port]; ok {
				hp = a
			}
			ip := b.HostIP
			if ip == "" {
				ip = "0.0.0.0"
			}
			list = append(list, map[string]any{"HostIp": ip, "HostPort": hp})
		}
		out[port] = list
	}
	return out
}

func splitPort(port string) (int, string) {
	num, proto, ok := strings.Cut(port, "/")
	if !ok {
		proto = "tcp"
	}
	return atoi(num), proto
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
