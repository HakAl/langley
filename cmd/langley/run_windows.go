//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func isTerminalFD(fd uintptr) bool {
	var mode uint32
	err := syscall.GetConsoleMode(syscall.Handle(fd), &mode)
	return err == nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p != nil
}

func lockFile(f *os.File) error {
	return nil
}

func unlockFile(f *os.File) error {
	return nil
}

// runProcess starts the command and waits with signal handling.
// On Windows, Ctrl+C is typically forwarded to child processes automatically.
func runProcess(ctx context.Context, cmd *exec.Cmd) int {
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to start %q: %v\n", cmd.Path, err)
		return 1
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer signal.Stop(sigChan)

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	for {
		select {
		case <-sigChan:
			// On Windows, Ctrl+C is automatically forwarded to child processes
			// in the same console. Wait for child to handle it, with timeout.
			select {
			case err := <-waitCh:
				return getExitCode(err)
			case <-time.After(5 * time.Second):
				// Child didn't exit after 5s, force kill and wait for exit
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				err := <-waitCh
				return getExitCode(err)
			}
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err := <-waitCh
			return getExitCode(err)
		case err := <-waitCh:
			return getExitCode(err)
		}
	}
}
