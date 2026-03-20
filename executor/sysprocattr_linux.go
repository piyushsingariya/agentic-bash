//go:build linux

package executor

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr configures the subprocess to:
//   - run in its own process group (Setpgid) so we can kill all descendants,
//   - receive SIGKILL automatically if the parent process dies (Pdeathsig).
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

// killProcessGroup sends SIGKILL to every process in the group whose leader
// has the given pid. The negative pid targets the whole group.
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
