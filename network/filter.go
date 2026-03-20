// Package network provides per-sandbox network access controls.
//
// Three modes are supported:
//
//	Allow     – full host network access (default, all platforms)
//	Deny      – no external traffic; only loopback reachable (Linux: CLONE_NEWNET; others: no-op + warning)
//	Allowlist – egress permitted only to listed domains/CIDRs (Linux: Deny + future veth/iptables; others: no-op + warning)
package network

import "os/exec"

// Filter applies network restrictions to each external command spawned by the
// sandbox exec handler.  It is called once per command via Wrap(), which
// mutates the cmd's SysProcAttr before cmd.Start().
type Filter interface {
	// Available reports whether this filter can be fully activated on the
	// current platform/kernel.  A filter that returns false should degrade
	// gracefully (e.g., log a warning and behave like Allow).
	Available() bool

	// Wrap mutates cmd's SysProcAttr to apply network restrictions.
	// Called after the isolation strategy's Wrap() so Cloneflags can be OR'd.
	Wrap(cmd *exec.Cmd) error
}

// noopFilter allows unrestricted network access.  Used for NetworkAllow and as
// the fallback on platforms where the requested mode is unsupported.
type noopFilter struct{}

func (n *noopFilter) Available() bool        { return true }
func (n *noopFilter) Wrap(_ *exec.Cmd) error { return nil }

// NewAllow returns a no-op filter that permits unrestricted network access.
func NewAllow() Filter { return &noopFilter{} }

// NewDeny returns a filter that blocks all external network traffic.
//
// On Linux it adds CLONE_NEWNET (plus CLONE_NEWUSER if not already set) to the
// child command so that it runs in an isolated network namespace with only a
// loopback interface.
//
// On non-Linux platforms it logs a warning to stderr and degrades to Allow.
func NewDeny() Filter { return newDenyFilter() }

// NewAllowlist returns a filter that permits egress only to the listed
// domains/CIDRs.
//
// On Linux the current implementation degrades to full Deny (CLONE_NEWNET),
// because setting up a veth pair + iptables rules requires CAP_NET_ADMIN.
// The allowed list is stored for forward-compatibility when a privileged helper
// or ambient capability is available.
//
// On non-Linux platforms it degrades to Allow with a warning.
func NewAllowlist(allowed []string) Filter { return newAllowlistFilter(allowed) }
