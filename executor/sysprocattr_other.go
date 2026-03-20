//go:build !linux

package executor

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr places the subprocess in its own process group so that
// killProcessGroup can reach all its descendants.
// Note: Pdeathsig is Linux-only; on macOS/BSD the parent-death signal is not
// available at the OS level, but the context-based kill in native.go still
// terminates the process group correctly.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// killProcessGroup sends SIGKILL to the process group identified by pid.
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
