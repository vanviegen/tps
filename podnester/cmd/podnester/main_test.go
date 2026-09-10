package main

import "testing"

func TestScanRun(t *testing.T) {
	name, userns, so, detach := scanRun([]string{"--rm", "-dit", "--name=db", "--userns", "keep-id", "--security-opt", "label=disable", "-e", "A=b", "-v", "/x:/y", "alpine", "-d"})
	if name != "db" || userns != "keep-id" || len(so) != 1 || so[0] != "label=disable" || !detach {
		t.Errorf("got %q %q %v %v", name, userns, so, detach)
	}
	if _, _, _, detach := scanRun([]string{"-it", "-eDEBUG=1", "--name", "x", "alpine"}); detach {
		t.Error("detach seen where there is none")
	}
	if _, _, _, detach := scanRun([]string{"--detach", "alpine"}); !detach {
		t.Error("--detach not seen")
	}
}
