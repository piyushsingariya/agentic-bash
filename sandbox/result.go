package sandbox

import "time"

// ExecutionResult contains the complete output and metadata for one Run() call.
type ExecutionResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration

	// Error is non-nil for infrastructure failures (timeout, closed sandbox, etc.).
	// A non-zero ExitCode with a nil Error means the command ran but exited non-zero.
	Error error

	// Filesystem diff recorded during this Run() (populated in Phase 3).
	FilesCreated  []string
	FilesModified []string
	FilesDeleted  []string

	// Resource usage metrics (populated in Phase 5, Linux only).
	CPUTime      time.Duration
	MemoryPeakMB int
}
