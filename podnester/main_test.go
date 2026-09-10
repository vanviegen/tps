package podnester

import (
	"fmt"
	"os"
	"testing"
)

// The test binary stands in for the binary embedding the proxy: the proxy
// runs it under podman unshare to resolve paths, and in the owner to
// forward ports, so it must take those roles like a real one.
func TestMain(m *testing.M) {
	if handled, err := Subcommand(os.Args); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}
