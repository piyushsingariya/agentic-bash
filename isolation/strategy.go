// Package isolation provides pluggable OS-level isolation strategies for
// sandbox execution.  All strategies satisfy the IsolationStrategy interface;
// the correct one for the current platform is selected at runtime via
// BestAvailable() or SelectStrategy().
package isolation

import "os/exec"

// IsolationLevel controls the degree of OS-level isolation applied to each Run().
type IsolationLevel int

const (
	// IsolationNone applies no OS-level isolation.
	IsolationNone IsolationLevel = iota

	// IsolationNamespace uses Linux mount/PID/user namespaces.
	IsolationNamespace

	// IsolationLandlock uses the Landlock LSM (Linux 5.13+, no root required).
	IsolationLandlock

	// IsolationAuto selects the strongest strategy available at runtime.
	IsolationAuto
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
func BestAvailable() IsolationStrategy {
	if ls := newLandlock(); ls.Available() {
		return ls
	}
	if ns := newNamespace(); ns.Available() {
		return ns
	}
	return NewNoop()
}

// SelectStrategy returns the strategy that corresponds to the given level.
// Unknown levels fall back to Noop.
func SelectStrategy(level IsolationLevel) IsolationStrategy {
	switch level {
	case IsolationNone:
		return NewNoop()
	case IsolationNamespace:
		return newNamespace()
	case IsolationLandlock:
		return newLandlock()
	case IsolationAuto:
		return BestAvailable()
	default:
		return NewNoop()
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
