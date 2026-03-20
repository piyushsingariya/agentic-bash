package sandbox

import (
	"io/fs"
	"time"
)

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

// FileInfo describes a single entry in the sandbox filesystem.
// Returned by Sandbox.ListFiles.
type FileInfo struct {
	Name    string      // base name of the entry
	Path    string      // absolute path within the sandbox root
	Size    int64       // size in bytes (0 for directories)
	Mode    fs.FileMode // file mode and permissions
	ModTime time.Time   // last modification time
	IsDir   bool        // true if the entry is a directory
}
