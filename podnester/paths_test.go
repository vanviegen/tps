package podnester

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	mk := func(p string) { os.MkdirAll(p, 0o755) }
	mk(filepath.Join(root, "etc"))
	os.WriteFile(filepath.Join(root, "etc", "passwd"), nil, 0o644)
	mk(filepath.Join(root, "home", "dev"))
	mk(filepath.Join(work, "src"))
	os.Symlink("/etc/passwd", filepath.Join(work, "esc"))            // absolute: the container's /etc
	os.Symlink("../../home/dev", filepath.Join(work, "src", "up"))   // relative, climbing out of the mount into the root
	os.Symlink("src", filepath.Join(work, "rel"))                    // relative within the mount
	os.Symlink("/work/src", filepath.Join(root, "home", "dev", "w")) // from the root into the mount
	os.Symlink("loop", filepath.Join(work, "loop"))                  // a loop

	tab := &mountTable{Root: root, Mounts: map[string]string{"/work": work}}
	cases := map[string]string{
		"/work":                  work,
		"/work/src":              filepath.Join(work, "src"),
		"/work/new/dir":          filepath.Join(work, "new", "dir"),
		"/etc/passwd":            filepath.Join(root, "etc", "passwd"),
		"/work/esc":              filepath.Join(root, "etc", "passwd"),
		"/work/src/up":           filepath.Join(root, "home", "dev"),
		"/work/src/up/w":         filepath.Join(work, "src"),
		"/work/rel/up/w/../../x": filepath.Join(root, "x"), // …/w is /work/src; two up is /
		"/nonexistent/../work":   work,
		"/":                      root,
		"/work/../etc":           filepath.Join(root, "etc"),
	}
	for in, want := range cases {
		got, err := tab.resolve(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
		} else if got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	if _, err := tab.resolve("/work/loop"); err == nil {
		t.Error("a symlink loop resolved")
	}
	if _, err := tab.resolve("relative"); err == nil {
		t.Error("a relative path resolved")
	}
	// A root that is not visible: paths under a mount still resolve, the root does not.
	hidden := &mountTable{Root: "/nonexistent/root", Mounts: map[string]string{"/work": work}}
	if got, err := hidden.resolve("/work/src"); err != nil || got != filepath.Join(work, "src") {
		t.Errorf("hidden root, mount path: %s, %v", got, err)
	}
	if _, err := hidden.resolve("/etc/passwd"); err != errNeedRoot {
		t.Errorf("hidden root, root path: %v", err)
	}
	if _, err := hidden.resolve("/work/esc"); err != errNeedRoot {
		t.Errorf("hidden root, link into root: %v", err)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, path string
		ok            bool
		params        map[string]string
	}{
		{"/_ping", "/_ping", true, map[string]string{}},
		{"/containers/{id}/json", "/containers/abc/json", true, map[string]string{"id": "abc"}},
		{"/containers/{id}/json", "/containers/abc", false, nil},
		{"/containers/{id}", "/containers/abc/json", false, nil},
		{"/images/{name...}/json", "/images/docker.io/library/alpine:3/json", true, map[string]string{"name": "docker.io/library/alpine:3"}},
		{"/images/{name...}/json", "/images/json", false, nil},
	}
	for _, c := range cases {
		params, ok := matchPattern(c.pattern, c.path)
		if ok != c.ok {
			t.Errorf("%s vs %s: ok=%v", c.pattern, c.path, ok)
			continue
		}
		for k, v := range c.params {
			if params[k] != v {
				t.Errorf("%s vs %s: %s=%q", c.pattern, c.path, k, params[k])
			}
		}
	}
	if r, _ := matchRoute("GET", "/v1.41/containers/json"); r != nil {
		t.Error("the version prefix must be split off before matching")
	}
	if v, rest := splitVersion("/v1.41/containers/json"); v != "/v1.41" || rest != "/containers/json" {
		t.Errorf("splitVersion: %q %q", v, rest)
	}
	if v, rest := splitVersion("/containers/json"); v != "" || rest != "/containers/json" {
		t.Errorf("splitVersion: %q %q", v, rest)
	}
	if v, rest := splitVersion("/volumes"); v != "" || rest != "/volumes" {
		t.Errorf("splitVersion: %q %q", v, rest)
	}
}

func TestRewrite(t *testing.T) {
	v := map[string]any{
		"Names":           []any{"/pre-db"},
		"Name":            "pre-db",
		"NetworkSettings": map[string]any{"Networks": map[string]any{"pre-bridge": map[string]any{"Aliases": []any{"pre-x", "other"}}}},
		"Image":           "prefixless",
	}
	out := stripPrefix(v, "pre-").(map[string]any)
	if out["Names"].([]any)[0] != "/db" || out["Name"] != "db" || out["Image"] != "prefixless" {
		t.Errorf("stripPrefix: %v", out)
	}
	nets := out["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
	if _, ok := nets["bridge"]; !ok {
		t.Errorf("network keys: %v", nets)
	}
	f, _ := parseFilters(`{"label":{"a=b":true}}`)
	if len(f["label"]) != 1 || f["label"][0] != "a=b" {
		t.Errorf("old filter form: %v", f)
	}
}
