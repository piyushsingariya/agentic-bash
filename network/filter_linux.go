//go:build linux

package network

import (
	"os"
	"os/exec"
	"syscall"
)

// ─── Deny ────────────────────────────────────────────────────────────────────

type denyFilter struct{}

func newDenyFilter() Filter { return &denyFilter{} }

func (d *denyFilter) Available() bool { return true }

// Wrap adds CLONE_NEWNET to isolate the child into its own network namespace.
// If CLONE_NEWUSER has not already been set (e.g. because NoopStrategy is in
// use), it is added together with UID/GID mappings so that CLONE_NEWNET can be
// used without root privileges.
func (d *denyFilter) Wrap(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}

	// CLONE_NEWNET alone requires CAP_SYS_ADMIN.  Pairing it with
	// CLONE_NEWUSER grants the child a full capability set inside its own user
	// namespace, making CLONE_NEWNET unprivileged.
	if cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWUSER == 0 {
		uid := os.Getuid()
		gid := os.Getgid()
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		if cmd.SysProcAttr.UidMappings == nil {
			cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: uid, Size: 1},
			}
		}
		if cmd.SysProcAttr.GidMappings == nil {
			cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: gid, Size: 1},
			}
		}
	}

	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNET
	return nil
}

// ─── Allowlist ───────────────────────────────────────────────────────────────

// allowlistFilter currently degrades to full network deny because setting up a
// veth pair + iptables egress rules inside a user namespace requires
// CAP_NET_ADMIN in the host namespace, which the library does not assume.
//
// The allowed slice is preserved so that a future privileged-helper path or
// ambient-capability detection can act on it without changing the public API.
type allowlistFilter struct {
	allowed []string
	deny    *denyFilter
}

func newAllowlistFilter(allowed []string) Filter {
	return &allowlistFilter{allowed: allowed, deny: &denyFilter{}}
}

func (a *allowlistFilter) Available() bool { return true }

// Wrap applies full network deny.  The allowlist is not yet enforced; a warning
// is written to the command's Stderr informing the caller of the degradation.
func (a *allowlistFilter) Wrap(cmd *exec.Cmd) error {
	// Inform callers that allowlist is not yet enforced.
	if cmd.Stderr != nil {
		_, _ = cmd.Stderr.Write([]byte(
			"network: allowlist mode is not fully supported; " +
				"degrading to full deny (CLONE_NEWNET)\n",
		))
	}
	return a.deny.Wrap(cmd)
}
