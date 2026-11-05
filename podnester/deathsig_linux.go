package podnester

import "syscall"

// diesWithUs has a child terminated when this process ends.
func diesWithUs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM} }
