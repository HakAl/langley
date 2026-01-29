//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// runProcess starts the command and waits with proper signal handling.
// The child is placed in its own process group to prevent double-signal delivery.
func runProcess(cmd *exec.Cmd) int {
	// Put child in its own process group.
	// This prevents the TTY from sending Ctrl+C to both parent and child.
	// The parent becomes the sole forwarder of signals.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to start %q: %v\n", cmd.Path, err)
		return 1
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigChan)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	for {
		select {
		case sig := <-sigChan:
			// Forward signal to the child's PROCESS GROUP (negative PID).
			// This ensures if the child spawned its own children (e.g., shell script),
			// they all receive the signal.
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
			}
		case err := <-waitCh:
			return getExitCode(err)
		}
	}
}
