// Package cgroups provides a thin cgroupv2 abstraction for per-sandbox
// resource limits (memory, CPU).  On non-Linux platforms all operations are
// no-ops that report unavailability.
package cgroups

// Opts carries the resource limits to apply when creating a cgroup.
type Opts struct {
	MaxMemoryBytes int64   // 0 = no limit; written to memory.max
	CPUQuota       float64 // 0 = no limit; fraction of one CPU (0.5 = 50%)
}

// Cgroup represents a single cgroupv2 scope created for one subprocess.
type Cgroup interface {
	// AddPID moves the process into this cgroup.
	AddPID(pid int) error
	// Stop harvests final CPU/memory metrics and removes the cgroup directory.
	Stop() (cpuUsec uint64, memPeakBytes uint64, err error)
}

// Manager creates per-subprocess cgroups under the agentic-bash hierarchy.
type Manager interface {
	// Available reports whether cgroupv2 is usable on this host.
	Available() bool
	// New creates a cgroup with the given unique id and resource limits.
	New(id string, opts Opts) (Cgroup, error)
}

// NewManager returns the platform-appropriate Manager.  On non-Linux or when
// cgroupv2 is absent it returns a no-op Manager.
func NewManager() Manager { return newManager() }
