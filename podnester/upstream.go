package podnester

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// upstream is the podman API socket the proxy talks to on behalf of its
// clients: the real thing, reached over its unix socket.
type upstream struct {
	sock   string
	client *http.Client
	rt     http.RoundTripper
}

func newUpstream(sock string) *upstream {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableCompression: true,
		// Streams (logs, events, attach) stay open for as long as the client wants.
		ResponseHeaderTimeout: 0,
	}
	return &upstream{sock: sock, client: &http.Client{Transport: tr}, rt: tr}
}

// apiError is what podman answered when a request of ours failed: its status
// and the message from its JSON body.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("podman: %s (%d)", e.Message, e.Status) }

// call performs one request against the compat API and decodes a JSON
// answer into out (when out is not nil). Non-2xx answers become apiErrors.
func (u *upstream) call(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	target := "http://podman" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &msg)
		if msg.Message == "" {
			msg.Message = strings.TrimSpace(string(data))
		}
		return &apiError{resp.StatusCode, msg.Message}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func isNotFound(err error) bool {
	e, ok := err.(*apiError)
	return ok && e.Status == http.StatusNotFound
}

// The slices of podman's inspect answers the proxy reads.

type containerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		UsernsMode string `json:"UsernsMode"` // "private" for a user namespace of its own, "host" for none
	} `json:"HostConfig"`
	GraphDriver struct {
		Data map[string]string `json:"Data"`
	} `json:"GraphDriver"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type containerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

type networkInspect struct {
	Name       string            `json:"Name"`
	ID         string            `json:"Id"`
	Labels     map[string]string `json:"Labels"`
	Containers map[string]any    `json:"Containers"`
}

type volumeInspect struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

type execInspect struct {
	ContainerID string `json:"ContainerID"`
}

func (u *upstream) inspectContainer(ctx context.Context, ref string) (*containerInspect, error) {
	var c containerInspect
	if err := u.call(ctx, "GET", "/containers/"+url.PathEscape(ref)+"/json", nil, nil, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (u *upstream) inspectNetwork(ctx context.Context, ref string) (*networkInspect, error) {
	var n networkInspect
	if err := u.call(ctx, "GET", "/networks/"+url.PathEscape(ref), nil, nil, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func (u *upstream) inspectVolume(ctx context.Context, ref string) (*volumeInspect, error) {
	var v volumeInspect
	if err := u.call(ctx, "GET", "/volumes/"+url.PathEscape(ref), nil, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// listContainers lists (all, not just running) containers matching a label.
func (u *upstream) listContainers(ctx context.Context, label string) ([]containerSummary, error) {
	q := url.Values{"all": {"true"}, "filters": {labelFilter(label)}}
	var list []containerSummary
	err := u.call(ctx, "GET", "/containers/json", q, nil, &list)
	return list, err
}

func labelFilter(label string) string {
	data, _ := json.Marshal(map[string][]string{"label": {label}})
	return string(data)
}

// EnsureService makes sure a podman API service answers on sock: an existing
// one (a user's podman.socket, say) is used as it is; otherwise
// `podman system service` is started on it, as a child that dies with this
// process. Callers keep the returned command alive for as long as they need
// the socket; nil when the socket was there already.
func EnsureService(sock string) (*exec.Cmd, error) {
	if socketAnswers(sock) {
		return nil, nil
	}
	if err := os.MkdirAll(dir(sock), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(sock)
	cmd := exec.Command("podman", "system", "service", "--time=0", "unix://"+sock)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting podman system service: %w", err)
	}
	for i := 0; i < 100; i++ {
		if socketAnswers(sock) {
			return cmd, nil
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("podman system service did not come up on %s", sock)
}

func socketAnswers(sock string) bool {
	u := newUpstream(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return u.call(ctx, "GET", "/_ping", nil, nil, nil) == nil
}

func dir(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

// execOutput runs a command in a container and returns what it printed,
// through the API's exec: created, started attached, and read back. The
// answer is docker's multiplexed stream (8-byte frame headers), demuxed.
func (u *upstream) execOutput(ctx context.Context, container string, cmd []string) (string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	if err := u.call(ctx, "POST", "/containers/"+url.PathEscape(container)+"/exec", nil, map[string]any{
		"Cmd": cmd, "AttachStdout": true, "AttachStderr": true,
	}, &created); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	req, err := http.NewRequestWithContext(ctx, "POST", "http://podman/exec/"+created.ID+"/start", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", &apiError{resp.StatusCode, strings.TrimSpace(string(raw))}
	}
	var out strings.Builder
	for len(raw) >= 8 {
		n := int(binary.BigEndian.Uint32(raw[4:8]))
		if 8+n > len(raw) {
			break
		}
		if raw[0] == 1 { // stdout
			out.Write(raw[8 : 8+n])
		}
		raw = raw[8+n:]
	}
	var insp struct {
		ExitCode int `json:"ExitCode"`
	}
	if err := u.call(ctx, "GET", "/exec/"+created.ID+"/json", nil, nil, &insp); err == nil && insp.ExitCode != 0 {
		return "", fmt.Errorf("%s exited with %d", strings.Join(cmd, " "), insp.ExitCode)
	}
	return out.String(), nil
}
