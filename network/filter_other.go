//go:build !linux

package network

import (
	"fmt"
	"os"
	"os/exec"
)

// On non-Linux platforms, network namespaces (CLONE_NEWNET) are unavailable.
// Both Deny and Allowlist degrade to the no-op Allow filter and print a
// one-time warning to stderr.

type denyFilter struct {
	warned bool
}

func newDenyFilter() Filter { return &denyFilter{} }

func (d *denyFilter) Available() bool { return false }

func (d *denyFilter) Wrap(_ *exec.Cmd) error {
	if !d.warned {
		fmt.Fprintln(os.Stderr, "network: NetworkDeny is not supported on this platform; degrading to Allow")
		d.warned = true
	}
	return nil
}

type allowlistFilter struct {
	allowed []string
	warned  bool
}

func newAllowlistFilter(allowed []string) Filter {
	return &allowlistFilter{allowed: allowed}
}

func (a *allowlistFilter) Available() bool { return false }

func (a *allowlistFilter) Wrap(_ *exec.Cmd) error {
	if !a.warned {
		fmt.Fprintf(os.Stderr,
			"network: NetworkAllowlist is not supported on this platform; degrading to Allow (allowlist: %v)\n",
			a.allowed,
		)
		a.warned = true
	}
	return nil
}
