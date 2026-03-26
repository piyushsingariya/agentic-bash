package sbfs

import (
	stdfs "io/fs"
	"path/filepath"
	"syscall"

	"github.com/spf13/afero"
)

// MemoryFS is a fully in-memory filesystem backed by afero.MemMapFs.
// Every path is checked against the configured root; attempts to escape via
// "../" traversal are rejected with stdfs.ErrPermission.
type MemoryFS struct {
	root string
	afs  afero.Fs
}

// NewMemoryFS creates a MemoryFS rooted at root (must be an absolute path).
func NewMemoryFS(root string) *MemoryFS {
	return &MemoryFS{
		root: filepath.Clean(root),
		afs:  afero.NewMemMapFs(),
	}
}

// check returns an error if name is not contained within root after cleaning.
func (m *MemoryFS) check(name string) error {
	return checkContainment(m.root, name)
}

// Root returns the configured root path.
func (m *MemoryFS) Root() string { return m.root }

// Afero returns the underlying afero.Fs (used by LayeredFS and Snapshot).
func (m *MemoryFS) Afero() afero.Fs { return m.afs }

// Reset replaces the underlying store with a fresh empty one.
// Used by Restore and sandbox Reset().
func (m *MemoryFS) Reset() { m.afs = afero.NewMemMapFs() }

// OpenFile implements SandboxFS.
func (m *MemoryFS) OpenFile(name string, flag int, perm stdfs.FileMode) (afero.File, error) {
	if err := m.check(name); err != nil {
		return nil, err
	}
	return m.afs.OpenFile(name, flag, perm)
}

// Stat implements SandboxFS.
func (m *MemoryFS) Stat(name string) (stdfs.FileInfo, error) {
	if err := m.check(name); err != nil {
		return nil, err
	}
	return m.afs.Stat(name)
}

// MkdirAll implements SandboxFS.
func (m *MemoryFS) MkdirAll(path string, perm stdfs.FileMode) error {
	if err := m.check(path); err != nil {
		return err
	}
	return m.afs.MkdirAll(path, perm)
}

// Remove implements SandboxFS.
func (m *MemoryFS) Remove(name string) error {
	if err := m.check(name); err != nil {
		return err
	}
	return m.afs.Remove(name)
}

// RemoveAll implements SandboxFS.
func (m *MemoryFS) RemoveAll(path string) error {
	if err := m.check(path); err != nil {
		return err
	}
	return m.afs.RemoveAll(path)
}

// Rename implements SandboxFS.
func (m *MemoryFS) Rename(oldpath, newpath string) error {
	if err := m.check(oldpath); err != nil {
		return err
	}
	if err := m.check(newpath); err != nil {
		return err
	}
	return m.afs.Rename(oldpath, newpath)
}

// ReadFile implements SandboxFS.
func (m *MemoryFS) ReadFile(name string) ([]byte, error) {
	if err := m.check(name); err != nil {
		return nil, err
	}
	return afero.ReadFile(m.afs, name)
}

// WriteFile implements SandboxFS.
func (m *MemoryFS) WriteFile(name string, data []byte, perm stdfs.FileMode) error {
	if err := m.check(name); err != nil {
		return err
	}
	return afero.WriteFile(m.afs, name, data, perm)
}

// Symlink implements SandboxFS.
// MemoryFS is a purely in-memory store with no real filesystem backing, so
// symlinks are not supported. Callers that need symlinks should use OsFS.
func (m *MemoryFS) Symlink(_, _ string) error {
	return syscall.ENOTSUP
}

// ReadDir implements SandboxFS.
func (m *MemoryFS) ReadDir(name string) ([]stdfs.DirEntry, error) {
	if err := m.check(name); err != nil {
		return nil, err
	}
	infos, err := afero.ReadDir(m.afs, name)
	if err != nil {
		return nil, err
	}
	entries := make([]stdfs.DirEntry, len(infos))
	for i, info := range infos {
		entries[i] = stdfs.FileInfoToDirEntry(info)
	}
	return entries, nil
}
