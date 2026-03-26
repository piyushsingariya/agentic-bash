//go:build !linux

package sbfs

import (
	"errors"
	stdfs "io/fs"
	"os"
)

// openat2InRoot is not available on non-Linux platforms.
// It always returns errors.ErrUnsupported so callers fall back to afero.OsFs.
func openat2InRoot(_, _ string, _ int, _ stdfs.FileMode) (*os.File, error) {
	return nil, errors.ErrUnsupported
}
