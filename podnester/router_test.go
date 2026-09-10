package podnester

import (
	"testing"
)

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
