// Package isolation provides pluggable OS-level isolation strategies for
// sandbox execution.  All strategies satisfy the IsolationStrategy interface;
// the correct one for the current platform is selected at runtime via
// BestAvailable() or SelectStrategy().
package isolation

import "os/exec"

// IsolationLevel controls the degree of OS-level isolation applied to each Run().
type IsolationLevel int

const (
	// IsolationAuto is the zero value and the default. It selects the strongest
	// isolation strategy available at runtime: Landlock on Linux 5.13+, namespace
	// isolation on older Linux, and no-op on macOS/other platforms.
	//
	// Zero-value safety: Options{} without an explicit Isolation field gets IsolationAuto,
	// ensuring library users receive the best available isolation by default.
	IsolationAuto IsolationLevel = iota

	// IsolationNone applies no OS-level isolation. The sandbox provides only
	// shell-level illusions (virtual paths, sysinfo spoofing, shim interception).
	//
	// SECURITY WARNING: IsolationNone provides ZERO process containment. Any
	// external command that spawns child processes (python3, node, bash, etc.)
	// runs those grandchild processes with full host filesystem access,
	// bypassing all shims, block lists, and path rewriting. On Linux, prefer
	// IsolationAuto. On macOS, use PythonRuntimeWASM for Python containment.
	IsolationNone

	// IsolationNamespace uses Linux mount/PID/user namespaces (CLONE_NEWNS etc.).
	// All processes in the subtree — including grandchild processes spawned by
	// language runtimes — share the namespace and are therefore contained.
	IsolationNamespace

	// IsolationLandlock uses the Landlock LSM (Linux 5.13+, no root required).
	// Filesystem access rules apply to the calling process and all descendants,
	// including grandchild processes.
	IsolationLandlock
)

// IsolationStrategy describes a single isolation mechanism.
type IsolationStrategy interface {
	// Name returns a short identifier (e.g., "noop", "namespace", "landlock").
	Name() string

	// Available reports whether this strategy can actually be activated on the
	// current platform / kernel version.  A strategy that returns false from
	// Available must never be used — SelectStrategy and BestAvailable handle
	// this automatically.
	Available() bool

	// Wrap mutates cmd's SysProcAttr before cmd.Start() is called.  It is
	// invoked for every external command spawned by the sandbox ExecHandler.
	Wrap(cmd *exec.Cmd) error

	// Apply applies in-process restrictions to the calling OS thread / process.
	// Intended for Landlock ("restrict self") scenarios.  For library use this
	// is usually a no-op; call it only when the sandbox process is the whole
	// application.
	Apply() error
}

// BestAvailable probes the available strategies and returns the strongest one
// that works on the current platform/kernel.  Priority order:
//
//	Landlock (Linux 5.13+) → Namespace (Linux) → Noop (all platforms)
//
// Prefer BestAvailableWithRoot for production sandbox use.
func BestAvailable() IsolationStrategy { return BestAvailableWithRoot("", false) }

// BestAvailableWithRoot is like BestAvailable but passes sandboxRoot to the
// Landlock allowlist and enables chroot inside namespace isolation when
// enableChroot is true.
func BestAvailableWithRoot(sandboxRoot string, enableChroot bool) IsolationStrategy {
	if ls := newLandlock(sandboxRoot); ls.Available() {
		return ls
	}
	if ns := newNamespace(sandboxRoot, enableChroot); ns.Available() {
		return ns
	}
	return NewNoop()
}

// SelectStrategy returns the strategy that corresponds to the given level.
// Unknown levels fall back to Noop.  Prefer SelectStrategyWithRoot for
// production sandbox use.
func SelectStrategy(level IsolationLevel) IsolationStrategy {
	return SelectStrategyWithRoot(level, "", false)
}

// SelectStrategyWithRoot is like SelectStrategy but configures the strategy
// with sandboxRoot (added to Landlock allowlist; used as chroot root in
// namespace mode when enableChroot is true).
func SelectStrategyWithRoot(level IsolationLevel, sandboxRoot string, enableChroot bool) IsolationStrategy {
	switch level {
	case IsolationAuto:
		return BestAvailableWithRoot(sandboxRoot, enableChroot)
	case IsolationNone:
		return NewNoop()
	case IsolationNamespace:
		return newNamespace(sandboxRoot, enableChroot)
	case IsolationLandlock:
		return newLandlock(sandboxRoot)
	default:
		return BestAvailableWithRoot(sandboxRoot, enableChroot)
	}
}

// ─── Noop ────────────────────────────────────────────────────────────────────

// NewNoop returns a NoopStrategy that performs no isolation.  It is the default
// on macOS and is safe to use on any platform.
func NewNoop() IsolationStrategy { return &NoopStrategy{} }

// NoopStrategy satisfies IsolationStrategy with no-op implementations.
// Available() always returns true.
type NoopStrategy struct{}

func (n *NoopStrategy) Name() string           { return "noop" }
func (n *NoopStrategy) Available() bool        { return true }
func (n *NoopStrategy) Wrap(_ *exec.Cmd) error { return nil }
func (n *NoopStrategy) Apply() error           { return nil }
