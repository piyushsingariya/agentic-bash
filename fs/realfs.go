package sbfs

import (
	stdfs "io/fs"
	"path/filepath"

	"github.com/spf13/afero"
)

// OsFS is a SandboxFS backed by the real host OS filesystem, rooted at a
// given directory.  All operations are passed through to the real OS, so
// external commands (cat, grep, sed …) run as child processes can read and
// write the same files as shell redirections handled by the OpenHandler.
//
// Containment: paths are verified to stay within root before every operation;
// traversal attempts (../../etc/passwd) are rejected with stdfs.ErrPermission.
type OsFS struct {
	root string
	afs  afero.Fs // afero.OsFs — thin wrapper around os.*
}

// NewOsFS creates an OsFS rooted at root.  root must be an absolute path that
// already exists on the host filesystem.
func NewOsFS(root string) *OsFS {
	return &OsFS{
		root: filepath.Clean(root),
		afs:  afero.NewOsFs(),
	}
}

// Root returns the configured root path.
func (o *OsFS) Root() string { return o.root }

// Afero returns the underlying afero.Fs (used by Snapshot).
func (o *OsFS) Afero() afero.Fs { return o.afs }

// check verifies that name does not escape root after cleaning.
func (o *OsFS) check(name string) error {
	return checkContainment(o.root, name)
}

// ensureDir creates the parent directory of name if it does not exist.
func (o *OsFS) ensureDir(name string) {
	if dir := filepath.Dir(name); dir != "" && dir != "." && dir != name {
		_ = o.afs.MkdirAll(dir, 0o755)
	}
}

// OpenFile implements SandboxFS.
func (o *OsFS) OpenFile(name string, flag int, perm stdfs.FileMode) (afero.File, error) {
	if err := o.check(name); err != nil {
		return nil, err
	}
	if isWriteFlag(flag) {
		o.ensureDir(name)
	}
	return o.afs.OpenFile(name, flag, perm)
}

// Stat implements SandboxFS.
func (o *OsFS) Stat(name string) (stdfs.FileInfo, error) {
	if err := o.check(name); err != nil {
		return nil, err
	}
	return o.afs.Stat(name)
}

// MkdirAll implements SandboxFS.
func (o *OsFS) MkdirAll(path string, perm stdfs.FileMode) error {
	if err := o.check(path); err != nil {
		return err
	}
	return o.afs.MkdirAll(path, perm)
}

// Remove implements SandboxFS.
func (o *OsFS) Remove(name string) error {
	if err := o.check(name); err != nil {
		return err
	}
	return o.afs.Remove(name)
}

// RemoveAll implements SandboxFS.
func (o *OsFS) RemoveAll(path string) error {
	if err := o.check(path); err != nil {
		return err
	}
	return o.afs.RemoveAll(path)
}

// Rename implements SandboxFS.
func (o *OsFS) Rename(oldpath, newpath string) error {
	if err := o.check(oldpath); err != nil {
		return err
	}
	if err := o.check(newpath); err != nil {
		return err
	}
	return o.afs.Rename(oldpath, newpath)
}

// ReadFile implements SandboxFS.
func (o *OsFS) ReadFile(name string) ([]byte, error) {
	if err := o.check(name); err != nil {
		return nil, err
	}
	return afero.ReadFile(o.afs, name)
}

// WriteFile implements SandboxFS.
func (o *OsFS) WriteFile(name string, data []byte, perm stdfs.FileMode) error {
	if err := o.check(name); err != nil {
		return err
	}
	o.ensureDir(name)
	return afero.WriteFile(o.afs, name, data, perm)
}

// ReadDir implements SandboxFS.
func (o *OsFS) ReadDir(name string) ([]stdfs.DirEntry, error) {
	if err := o.check(name); err != nil {
		return nil, err
	}
	infos, err := afero.ReadDir(o.afs, name)
	if err != nil {
		return nil, err
	}
	entries := make([]stdfs.DirEntry, len(infos))
	for i, info := range infos {
		entries[i] = stdfs.FileInfoToDirEntry(info)
	}
	return entries, nil
}
