//go:build linux

package isolation

import (
	"os/exec"
	"syscall"
)

// initCmd sets the baseline SysProcAttr fields that apply to every externally
// executed command regardless of the active isolation strategy.
//
// On Linux this includes Pdeathsig so the child is automatically killed if the
// parent dies, eliminating zombie processes when the sandbox is abruptly torn
// down.
func initCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

// killProcessGroup sends SIGKILL to every process in the group whose leader
// has the given pid.  The negative pid targets the whole process group.
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
