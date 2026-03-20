// Package sbfs provides the layered, in-memory filesystem used by the sandbox
// execution environment.  Reads fall through a read-only base (lower) layer;
// writes always go to the writable in-memory upper layer, leaving the host
// filesystem untouched.
package sbfs

import (
	"fmt"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// SandboxFS is the interface satisfied by every filesystem implementation in
// this package.  All paths must be absolute; relative-path resolution is the
// caller's responsibility (the mvdan.cc/sh interpreter resolves them against
// the runner's Dir before invoking the OpenHandler).
type SandboxFS interface {
	// OpenFile opens or creates a file with the given OS flags and permission.
	// flag uses the same constants as os.OpenFile (os.O_RDONLY, os.O_WRONLY, etc.).
	OpenFile(name string, flag int, perm stdfs.FileMode) (afero.File, error)

	// Stat returns FileInfo for the named file.
	Stat(name string) (stdfs.FileInfo, error)

	// MkdirAll creates path and all necessary parents.
	MkdirAll(path string, perm stdfs.FileMode) error

	// Remove removes the named file or empty directory.
	Remove(name string) error

	// RemoveAll removes path and any children it contains.
	RemoveAll(path string) error

	// Rename renames (moves) oldpath to newpath.
	Rename(oldpath, newpath string) error

	// ReadFile reads and returns the contents of the named file.
	ReadFile(name string) ([]byte, error)

	// WriteFile writes data to the named file, creating it if necessary.
	WriteFile(name string, data []byte, perm stdfs.FileMode) error

	// ReadDir returns the sorted directory entries.
	ReadDir(name string) ([]stdfs.DirEntry, error)
}

// containedBy reports whether path is root or a descendant of root.
// Both arguments are expected to be filepath.Clean'd.
func containedBy(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// checkContainment returns an error if name is not contained within root after
// cleaning.  Used by every FS implementation to enforce sandbox boundaries.
func checkContainment(root, name string) error {
	if !containedBy(root, filepath.Clean(name)) {
		return fmt.Errorf("%w: path escapes sandbox root: %s", stdfs.ErrPermission, name)
	}
	return nil
}

// isWriteFlag reports whether flag contains any write-intent bit.
func isWriteFlag(flag int) bool {
	return flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
}
