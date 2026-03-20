//go:build !linux

package isolation

import (
	"os/exec"
	"syscall"
)

// initCmd sets baseline SysProcAttr fields on non-Linux platforms.
// Pdeathsig is Linux-only and is omitted here.
func initCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to the process group identified by pid.
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
