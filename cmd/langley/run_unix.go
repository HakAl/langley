//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func isTerminalFD(fd uintptr) bool {
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	return errno == 0
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

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

// collectDescendantTargets returns all process group IDs and PIDs belonging to
// rootPid and its descendants (including interactive shell jobs placed in their
// own process groups), excluding prevPgrp and the current process group.
func collectDescendantTargets(rootPid int, ttyFD int, prevPgrp int32) (pgids []int, pids []int) {
	selfPgrp := syscall.Getpgrp()
	pgidSet := make(map[int]bool)
	pidSet := make(map[int]bool)

	addPgid := func(pgid int) {
		if pgid > 0 && pgid != selfPgrp && pgid != int(prevPgrp) {
			pgidSet[pgid] = true
		}
	}
	addPid := func(pid int) {
		if pid > 0 && pid != os.Getpid() {
			pidSet[pid] = true
		}
	}

	addPgid(rootPid)
	addPid(rootPid)

	if ttyFD >= 0 {
		if fpgrp, err := tcgetpgrp(ttyFD); err == nil && fpgrp > 0 {
			addPgid(int(fpgrp))
		}
	}

	psBin := "/bin/ps"
	if _, err := os.Stat(psBin); err != nil {
		psBin = "ps"
	}
	psCmd := exec.Command(psBin, "-ax", "-o", "pid=,ppid=,pgid=")
	out, err := psCmd.Output()
	if err == nil {
		type procEntry struct {
			pid  int
			pgid int
		}
		children := make(map[int][]procEntry)
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			pid, err1 := strconv.Atoi(fields[0])
			ppid, err2 := strconv.Atoi(fields[1])
			pgid, err3 := strconv.Atoi(fields[2])
			if err1 != nil || err2 != nil || err3 != nil || pid <= 0 {
				continue
			}
			children[ppid] = append(children[ppid], procEntry{pid: pid, pgid: pgid})
		}

		queue := []int{rootPid}
		visited := map[int]bool{rootPid: true}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, ch := range children[cur] {
				addPid(ch.pid)
				addPgid(ch.pgid)
				if !visited[ch.pid] {
					visited[ch.pid] = true
					queue = append(queue, ch.pid)
				}
			}
		}
	}

	for pgid := range pgidSet {
		pgids = append(pgids, pgid)
	}
	for pid := range pidSet {
		pids = append(pids, pid)
	}
	return pgids, pids
}

func anyTargetAlive(pgids, pids []int) bool {
	for _, pgid := range pgids {
		if err := syscall.Kill(-pgid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			return true
		}
	}
	for _, pid := range pids {
		if isProcessAlive(pid) {
			return true
		}
	}
	return false
}

func anyNonRootTargetAlive(rootPid int, pgids, pids []int) bool {
	for _, pgid := range pgids {
		if pgid == rootPid {
			continue
		}
		if err := syscall.Kill(-pgid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			return true
		}
	}
	for _, pid := range pids {
		if pid == rootPid {
			continue
		}
		if isProcessAlive(pid) {
			return true
		}
	}
	return false
}

func isShellBinary(path string) bool {
	base := strings.TrimPrefix(filepath.Base(path), "-")
	switch base {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "csh", "tcsh":
		return true
	default:
		return false
	}
}

func signalTargets(rootPid int, pgids, pids []int, sig syscall.Signal) {
	for _, pgid := range pgids {
		if pgid != rootPid {
			_ = syscall.Kill(-pgid, sig)
		}
	}
	for _, pid := range pids {
		if pid != rootPid {
			_ = syscall.Kill(pid, sig)
		}
	}
	_ = syscall.Kill(-rootPid, sig)
}

func terminateProcessTree(cmd *exec.Cmd, ttyFD int, prevPgrp int32, sig syscall.Signal, waitCh <-chan error) int {
	if cmd.Process == nil {
		err := <-waitCh
		return getExitCode(err)
	}
	rootPid := cmd.Process.Pid
	pgids, pids := collectDescendantTargets(rootPid, ttyFD, prevPgrp)
	signalTargets(rootPid, pgids, pids, sig)

	deadline := time.Now().Add(5 * time.Second)
	hupTime := time.Now().Add(50 * time.Millisecond)
	sentHUP := sig == syscall.SIGHUP
	interactiveShell := ttyFD >= 0 && isShellBinary(cmd.Path)

	var waitDone bool
	var waitErr error

	for time.Now().Before(deadline) {
		if !waitDone {
			select {
			case waitErr = <-waitCh:
				waitDone = true
			default:
			}
		}
		if waitDone && !anyTargetAlive(pgids, pids) {
			return getExitCode(waitErr)
		}
		// Interactive shells (bash/zsh) ignore SIGTERM by default and exit on SIGHUP.
		// Only send SIGHUP to the shell process itself after all child jobs inside
		// the shell have completed their own graceful SIGTERM cleanup.
		if !sentHUP && interactiveShell && time.Now().After(hupTime) {
			morePGIDs, morePIDs := collectDescendantTargets(rootPid, ttyFD, prevPgrp)
			pgids = append(pgids, morePGIDs...)
			pids = append(pids, morePIDs...)
			if !anyNonRootTargetAlive(rootPid, pgids, pids) {
				_ = syscall.Kill(rootPid, syscall.SIGHUP)
				sentHUP = true
			}
		}
		time.Sleep(15 * time.Millisecond)
	}

	morePGIDs, morePIDs := collectDescendantTargets(rootPid, ttyFD, prevPgrp)
	pgids = append(pgids, morePGIDs...)
	pids = append(pids, morePIDs...)
	signalTargets(rootPid, pgids, pids, syscall.SIGKILL)

	if !waitDone {
		waitErr = <-waitCh
	}
	return getExitCode(waitErr)
}

// runProcess starts the command and waits with proper signal handling.
// The child is placed in its own process group so signals can be forwarded to
// the entire descendant tree. When running in the foreground of a controlling
// terminal, the child's process group is placed in the terminal foreground so
// interactive CLI tools (such as claude, codex, python, node) are not stopped
// by SIGTTIN/SIGTTOU.
func runProcess(ctx context.Context, cmd *exec.Cmd) int {
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
			s := sig.(syscall.Signal)
			if s == syscall.SIGTERM || s == syscall.SIGHUP {
				return terminateProcessTree(cmd, ttyFD, prevPgrp, s, waitCh)
			}
			if cmd.Process != nil {
				pgids, _ := collectDescendantTargets(cmd.Process.Pid, ttyFD, prevPgrp)
				for _, pgid := range pgids {
					_ = syscall.Kill(-pgid, s)
				}
			}
		case <-ctx.Done():
			return terminateProcessTree(cmd, ttyFD, prevPgrp, syscall.SIGTERM, waitCh)
		case err := <-waitCh:
			return getExitCode(err)
		}
	}
}
