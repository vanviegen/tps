//go:build !linux

package daemon

import (
	"errors"
	"os"
)

// openPty: the daemon runs on Linux only; elsewhere it is built into a
// dashboard that never starts it.
func openPty() (master, slave *os.File, err error) {
	return nil, nil, errors.New("pseudo-terminals are opened on Linux only")
}
