//go:build !linux

package podnester

import "syscall"

// diesWithUs: only Linux has a child die with its parent; podnester runs
// there, and builds elsewhere only as part of a dashboard that never starts it.
func diesWithUs() *syscall.SysProcAttr { return nil }
