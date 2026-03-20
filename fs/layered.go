package sbfs

import (
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// LayeredFS is the sandbox filesystem used during execution.
//
// When baseDir is non-empty, its contents are pre-copied into root so they are
// immediately available inside the sandbox.  All subsequent reads and writes
// target the real host directory at root, which means external commands (cat,
// grep, sed …) run as child processes can access the same files as shell
// redirections handled by our OpenHandler.
type LayeredFS struct {
	*OsFS // real-filesystem layer rooted at the sandbox tmpDir
}

// NewLayeredFS creates a LayeredFS rooted at root (an existing host directory).
// If baseDir is non-empty its contents are recursively copied into root.
func NewLayeredFS(root, baseDir string) *LayeredFS {
	upper := NewOsFS(root)

	if baseDir != "" {
		_ = afero.Walk(afero.NewOsFs(), baseDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			rel, relErr := filepath.Rel(baseDir, path)
			if relErr != nil {
				return nil
			}
			target := filepath.Join(root, rel)
			if info.IsDir() {
				_ = os.MkdirAll(target, info.Mode())
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			_ = os.WriteFile(target, data, info.Mode())
			return nil
		})
	}

	return &LayeredFS{OsFS: upper}
}

// Upper returns the underlying OsFS (used by ChangeTracker and Snapshot).
func (l *LayeredFS) Upper() *OsFS { return l.OsFS }

// Clear removes all files and directories inside Root() without removing Root
// itself.  Used by sandbox.Reset() to wipe the filesystem between sessions.
func (l *LayeredFS) Clear() error {
	entries, err := os.ReadDir(l.Root())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if rmErr := os.RemoveAll(filepath.Join(l.Root(), e.Name())); rmErr != nil {
			return rmErr
		}
	}
	return nil
}
