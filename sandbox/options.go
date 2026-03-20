package sandbox

import (
	"time"

	"github.com/piyushsingariya/agentic-bash/isolation"
)

// Re-export isolation levels for callers who only import the sandbox package.
type IsolationLevel = isolation.IsolationLevel

const (
	IsolationNone      = isolation.IsolationNone
	IsolationNamespace = isolation.IsolationNamespace
	IsolationLandlock  = isolation.IsolationLandlock
	IsolationAuto      = isolation.IsolationAuto
)

// NetworkMode controls outbound network access from the sandbox.
type NetworkMode int

const (
	// NetworkAllow grants full host network access (default).
	NetworkAllow NetworkMode = iota

	// NetworkDeny blocks all external network access; only loopback is reachable (Phase 7).
	NetworkDeny

	// NetworkAllowlist permits egress only to the domains/CIDRs listed in NetworkPolicy.Allowlist (Phase 7).
	NetworkAllowlist
)

// NetworkPolicy configures network restrictions for a sandbox.
type NetworkPolicy struct {
	Mode      NetworkMode
	Allowlist []string // domains or CIDR ranges; meaningful when Mode == NetworkAllowlist
	DNSServer string   // custom resolver address; empty means system default
}

// ResourceLimits caps the resources a single Run() call may consume.
type ResourceLimits struct {
	// Timeout is the wall-clock limit per Run(). Zero is replaced by DefaultTimeout.
	Timeout time.Duration

	// MaxMemoryMB limits peak resident memory via cgroupv2 (Linux only; Phase 5).
	// Zero means no limit.
	MaxMemoryMB int

	// MaxCPUPercent caps CPU usage as a fraction of one core (Linux only; Phase 5).
	// Zero means no limit. Example: 50.0 = 50% of one CPU.
	MaxCPUPercent float64

	// MaxOutputMB caps the combined stdout+stderr size (Phase 5).
	// Zero means no limit.
	MaxOutputMB int

	// MaxFileSizeMB caps any single file write inside the sandbox (Phase 3).
	// Zero means no limit.
	MaxFileSizeMB int
}

// PolicyViolation is sent to Options.OnViolation when a policy rule fires.
type PolicyViolation struct {
	Type    string // "network", "filesystem", "syscall"
	Detail  string
	Blocked bool // true if the operation was prevented; false if only logged
}

// Options configures a Sandbox at creation time. All fields are optional;
// zero values produce a working sandbox with sensible defaults.
type Options struct {
	Isolation IsolationLevel
	Limits    ResourceLimits
	Network   NetworkPolicy

	// Env is the initial set of environment variables visible to all Run() calls.
	// Variables are merged into the subprocess environment; existing host env is
	// not inherited unless explicitly included here.
	Env map[string]string

	// WorkDir is the starting working directory inside the sandbox.
	// Defaults to the sandbox's own temp directory when empty.
	WorkDir string

	// BaseImageDir is the path to a pre-baked directory that forms the read-only
	// lower layer of the sandbox filesystem (Phase 3). Empty disables layering.
	BaseImageDir string

	// Hooks — called synchronously on the goroutine that invoked Run().
	OnCommand   func(cmd string)
	OnResult    func(r ExecutionResult)
	OnViolation func(v PolicyViolation)
}
