package daemon

import (
	"slices"
	"testing"
)

func TestContainerfileCmd(t *testing.T) {
	cf := "FROM a\nCMD [\"old\"]\nFROM b\n# CMD nope\nRUN echo CMD\n  cmd npm \\\nrun dev\n"
	if got := containerfileCmd(cf); !slices.Equal(got, []string{"/bin/sh", "-c", "npm run dev"}) || cmdDisplay(got) != "npm run dev" {
		t.Errorf("shell form: %q", got)
	}
	if got := containerfileCmd("FROM x\nCMD [\"node\", \"a b.js\"]\n"); !slices.Equal(got, []string{"node", "a b.js"}) || cmdDisplay(got) != `node "a b.js"` {
		t.Errorf("exec form: %q", got)
	}
	if containerfileCmd(defaultContainerfile) != nil || cmdDisplay(nil) != "" {
		t.Error("no CMD")
	}
}

func TestPublishedPorts(t *testing.T) {
	text := `{"9000/tcp":[{"HostIp":"127.0.0.1","HostPort":"41001"}],"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"41002"}],` +
		`"53/udp":[{"HostIp":"127.0.0.1","HostPort":"41003"}],"3000/tcp":null}`
	got := parsePublishedPorts(text)
	if !slices.Equal(got, []PortMap{{8080, 41002}, {9000, 41001}}) {
		t.Errorf("published ports: %v", got)
	}
	if parsePublishedPorts("null") != nil || parsePublishedPorts("") != nil {
		t.Error("no bindings")
	}
	if port, proto := splitPortSpec("8080"); port != 8080 || proto != "tcp" {
		t.Errorf("bare spec: %d/%s", port, proto)
	}
	if port, proto := splitPortSpec("53/UDP"); port != 53 || proto != "udp" {
		t.Errorf("udp spec: %d/%s", port, proto)
	}
}
