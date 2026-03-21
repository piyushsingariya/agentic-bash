package intercept

import (
	"context"
	"io/fs"
	"os"

	"mvdan.cc/sh/v3/interp"

	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
)

// NewStatHandler returns a StatHandlerFunc that translates virtual absolute
// paths to their real on-disk counterparts before calling os.Stat / os.Lstat.
//
// This fixes shell conditionals like [[ -f /home/user/foo ]] and [ -d /workspace ]
// which use stat() internally — without translation they operate on host paths
// that don't exist.
func NewStatHandler(sandboxRoot string) interp.StatHandlerFunc {
	return func(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
		real := pathmap.VirtualToReal(sandboxRoot, name)
		if followSymlinks {
			return os.Stat(real)
		}
		return os.Lstat(real)
	}
}

// NewReadDirHandler returns a ReadDirHandlerFunc2 that translates virtual
// absolute paths to real before reading directory contents.
//
// This fixes shell glob expansion: when the shell expands /workspace/*.py it
// calls ReadDir("/workspace"). Without translation it would read the host
// /workspace which doesn't exist (or is the wrong directory).
func NewReadDirHandler(sandboxRoot string) interp.ReadDirHandlerFunc2 {
	return func(ctx context.Context, path string) ([]fs.DirEntry, error) {
		real := pathmap.VirtualToReal(sandboxRoot, path)
		return os.ReadDir(real)
	}
}
