//go:build linux

package isolation

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// NamespaceStrategy isolates each external command inside a new set of Linux
// namespaces:
//
//   - CLONE_NEWUSER  – unprivileged user namespace; maps the child's root (uid 0)
//     to the calling process's real UID so no real privileges are granted.
//   - CLONE_NEWNS    – mount namespace; the child cannot affect host mount points.
//   - CLONE_NEWPID   – PID namespace; sandbox processes cannot signal host pids.
//
// When sandboxRoot is non-empty and enableChroot is true, each subprocess is
// additionally chrooted into sandboxRoot so symlinks pointing to absolute host
// paths resolve within the sandbox rather than the real filesystem.
// Requires a fully-populated BaseImageDir (all executed binaries must exist
// inside sandboxRoot).
//
// This strategy requires no root privileges on kernels that allow unprivileged
// user-namespace creation (all major distros since ~2016).
type NamespaceStrategy struct {
	sandboxRoot  string
	enableChroot bool
}

func newNamespace(sandboxRoot string, enableChroot bool) IsolationStrategy {
	return &NamespaceStrategy{sandboxRoot: sandboxRoot, enableChroot: enableChroot}
}

// NewNamespaceForTest returns a NamespaceStrategy; used in cross-platform tests.
func NewNamespaceForTest() IsolationStrategy { return &NamespaceStrategy{} }

func (n *NamespaceStrategy) Name() string { return "namespace" }

// Available probes whether unprivileged user namespaces are permitted.
// It checks the kernel knob at /proc/sys/kernel/unprivileged_userns_clone
// (Debian/Ubuntu) and also tries the standard approach of checking
// /proc/sys/user/max_user_namespaces.
func (n *NamespaceStrategy) Available() bool {
	// Some distros gate unprivileged user namespaces with this sysctl.
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if len(data) > 0 && data[0] == '0' {
			return false
		}
	}
	// If max_user_namespaces is 0, user namespaces are disabled.
	if data, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if len(data) > 0 && data[0] == '0' {
			return false
		}
	}
	return true
}

// Wrap applies namespace Cloneflags and UID/GID mappings to cmd.
// Must be called after initCmd() so that SysProcAttr already exists.
// When enableChroot is true and sandboxRoot is set, the subprocess is also
// chrooted into sandboxRoot so absolute symlinks cannot escape to the host.
func (n *NamespaceStrategy) Wrap(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}

	uid := os.Getuid()
	gid := os.Getgid()

	cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER |
		syscall.CLONE_NEWNS |
		syscall.CLONE_NEWPID

	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: uid, Size: 1},
	}
	cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
		{ContainerID: 0, HostID: gid, Size: 1},
	}

	if n.enableChroot && n.sandboxRoot != "" {
		cmd.SysProcAttr.Chroot = n.sandboxRoot
		// Rewrite Dir to be relative to the new root.
		if strings.HasPrefix(cmd.Dir, n.sandboxRoot) {
			cmd.Dir = cmd.Dir[len(n.sandboxRoot):]
			if cmd.Dir == "" {
				cmd.Dir = "/"
			}
		} else {
			cmd.Dir = "/"
		}
	}

	return nil
}

// Apply is a no-op for NamespaceStrategy; namespaces are applied per-subprocess
// via Wrap(), not in the calling process.
func (n *NamespaceStrategy) Apply() error { return nil }

