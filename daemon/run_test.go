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
