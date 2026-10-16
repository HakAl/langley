//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

func tcgetpgrp(fd int) (int32, error) {
	var pgid int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&pgid)))
	if errno != 0 {
		return -1, errno
	}
	return pgid, nil
}

func tcsetpgrp(fd int, pgid int32) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCSPGRP), uintptr(unsafe.Pointer(&pgid)))
	if errno != 0 {
		return errno
	}
	return nil
}

// runProcess starts the command and waits with proper signal handling.
// The child is placed in its own process group so signals can be forwarded to
// the entire descendant tree. When running in the foreground of a controlling
// terminal, the child's process group is placed in the terminal foreground so
// interactive CLI tools (such as claude, codex, python, node) are not stopped
// by SIGTTIN/SIGTTOU.
func runProcess(cmd *exec.Cmd) int {
	sysAttr := &syscall.SysProcAttr{
		Setpgid: true,
	}

	var tty *os.File
	ttyFD := -1
	var prevPgrp int32
	if cmd.Stdin == os.Stdin && os.Stdin != nil {
		stdinFD := int(os.Stdin.Fd())
		if fpgrp, err := tcgetpgrp(stdinFD); err == nil && fpgrp > 0 && int(fpgrp) == syscall.Getpgrp() {
			if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
				tty = f
				ttyFD = int(f.Fd())
			} else {
				ttyFD = stdinFD
			}
			prevPgrp = fpgrp
		}
	}

	if ttyFD >= 0 {
		signal.Ignore(syscall.SIGTTOU, syscall.SIGTTIN)
		sysAttr.Foreground = true
		sysAttr.Ctty = ttyFD
		defer func() {
			_ = tcsetpgrp(ttyFD, prevPgrp)
			if tty != nil {
				_ = tty.Close()
			}
			signal.Reset(syscall.SIGTTOU, syscall.SIGTTIN)
		}()
	}

	cmd.SysProcAttr = sysAttr

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
