package sandbox

import (
	"io"
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

// BootstrapConfig controls the synthetic Linux identity of the sandbox.
// Zero values are replaced by defaults in sandbox.New().
type BootstrapConfig struct {
	UserName string // default "user"
	Hostname string // default "sandbox"
	UID      int    // default 1000
	GID      int    // default 1000
}

// EnvironmentPreset controls how the initial shell environment is constructed.
type EnvironmentPreset int

const (
	// EnvPresetLinux builds a clean synthetic Linux environment (default).
	// The host process environment is NOT inherited. Variables in Options.Env
	// are merged on top of the Linux base set.
	EnvPresetLinux EnvironmentPreset = iota

	// EnvPresetInheritHost inherits the full host process environment, then
	// merges Options.Env on top. Matches the old default behaviour.
	EnvPresetInheritHost

	// EnvPresetEmpty starts with only Options.Env set; nothing else.
	EnvPresetEmpty
)

// PythonRuntimeMode controls how python3/python commands are executed.
type PythonRuntimeMode int

const (
	// PythonRuntimeNative (default) uses the host python3 binary.
	// The subprocess module works, but grandchild processes spawned by Python
	// run outside the sandbox intercept chain and can access the host filesystem.
	// On Linux, OS-level isolation (IsolationNamespace/Landlock) still contains
	// grandchildren at the kernel level. On macOS, there is no containment.
	PythonRuntimeNative PythonRuntimeMode = iota

	// PythonRuntimeWASM runs Python inside a wazero WASI runtime.
	// subprocess.run(), os.system(), and os.popen() all fail with ENOSYS —
	// subprocess escape is impossible by WASI specification, on every platform.
	// Filesystem access is scoped to the sandbox overlay.
	// C extensions (numpy native code, etc.) cannot be loaded.
	// Requires PythonWASMBytes to be set; obtain via packages.FetchPythonWASM().
	PythonRuntimeWASM
)

// Options configures a Sandbox at creation time. All fields are optional;
// zero values produce a working sandbox with sensible defaults.
type Options struct {
	Isolation IsolationLevel
	Limits    ResourceLimits
	Network   NetworkPolicy

	// Bootstrap controls the synthetic Linux identity (username, hostname, etc.)
	// used when EnvPreset is EnvPresetLinux (the default).
	Bootstrap BootstrapConfig

	// EnvPreset selects how the initial environment is built.
	// Defaults to EnvPresetLinux.
	EnvPreset EnvironmentPreset

	// Env is merged on top of the preset environment. Use this to add or
	// override individual variables without changing the preset.
	Env map[string]string

	// WorkDir is the starting working directory inside the sandbox.
	// Defaults to /home/{Bootstrap.UserName} for EnvPresetLinux, or the
	// sandbox temp directory for other presets.
	WorkDir string

	// BaseImageDir is the path to a pre-baked directory that forms the read-only
	// lower layer of the sandbox filesystem (Phase 3). Empty disables layering.
	BaseImageDir string

	// AuditWriter receives a timestamped log line for every command invocation
	// (including shell builtins) when non-nil. Format: [HH:MM:SS.mmm] call: <args>
	AuditWriter io.Writer

	// BlockList is a list of command patterns denied unconditionally.
	// Each entry is prefix-matched against the full command+args joined by spaces.
	// Example: []string{"rm -rf /", "mkfs", "dd if=/dev/"}
	BlockList []string

	// PythonRuntime selects how python3/python commands execute.
	// Defaults to PythonRuntimeNative.
	// Set to PythonRuntimeWASM for hard subprocess containment without
	// requiring OS-level isolation — effective on macOS and Linux.
	PythonRuntime PythonRuntimeMode

	// PythonWASMBytes is the raw bytes of a WASI-compiled python.wasm binary.
	// Required when PythonRuntime == PythonRuntimeWASM; ignored otherwise.
	// Obtain via packages.FetchPythonWASM() or embed your own build.
	PythonWASMBytes []byte

	// Hooks — called synchronously on the goroutine that invoked Run().
	OnCommand   func(cmd string)
	OnResult    func(r ExecutionResult)
	OnViolation func(v PolicyViolation)
}
