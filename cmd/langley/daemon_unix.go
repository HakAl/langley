//go:build !windows

package main

import "syscall"

// detachedProcAttr starts the daemon in its own session so closing the
// launching terminal does not deliver SIGHUP to it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminateProcess asks pid to shut down gracefully.
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
