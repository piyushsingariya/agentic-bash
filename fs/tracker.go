package sbfs

import (
	stdfs "io/fs"
	"sync"

	"github.com/spf13/afero"
)

// ChangeTracker wraps any SandboxFS and records which files were created,
// modified, or deleted during a single Run() interval.
//
// Call Reset() before each Run() to clear the previous interval's changes.
// Call Changes() after Run() to retrieve the accumulated diff.
type ChangeTracker struct {
	inner SandboxFS

	mu       sync.Mutex
	created  map[string]bool
	modified map[string]bool
	deleted  map[string]bool
}

// NewChangeTracker returns a ChangeTracker wrapping inner.
func NewChangeTracker(inner SandboxFS) *ChangeTracker {
	return &ChangeTracker{
		inner:    inner,
		created:  make(map[string]bool),
		modified: make(map[string]bool),
		deleted:  make(map[string]bool),
	}
}

// Reset clears all accumulated change records.  Call before each Run().
func (c *ChangeTracker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created = make(map[string]bool)
	c.modified = make(map[string]bool)
	c.deleted = make(map[string]bool)
}

// FilesCreated returns paths created during the current interval.
func (c *ChangeTracker) FilesCreated() []string { return c.keys(c.created) }

// FilesModified returns paths modified during the current interval.
func (c *ChangeTracker) FilesModified() []string { return c.keys(c.modified) }

// FilesDeleted returns paths deleted during the current interval.
func (c *ChangeTracker) FilesDeleted() []string { return c.keys(c.deleted) }

func (c *ChangeTracker) keys(m map[string]bool) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// recordWrite records a write to name.  If name did not exist before the
// write it is classified as Created; otherwise as Modified.
func (c *ChangeTracker) recordWrite(name string) {
	_, existErr := c.inner.Stat(name) // I/O outside lock
	c.mu.Lock()
	defer c.mu.Unlock()
	if existErr != nil {
		c.created[name] = true
	} else {
		if !c.created[name] {
			c.modified[name] = true
		}
	}
}

// recordDelete records that name was removed.
func (c *ChangeTracker) recordDelete(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.created, name)
	delete(c.modified, name)
	c.deleted[name] = true
}

// OpenFile implements SandboxFS.
func (c *ChangeTracker) OpenFile(name string, flag int, perm stdfs.FileMode) (afero.File, error) {
	if isWriteFlag(flag) {
		c.recordWrite(name)
	}
	return c.inner.OpenFile(name, flag, perm)
}

// Stat implements SandboxFS.
func (c *ChangeTracker) Stat(name string) (stdfs.FileInfo, error) {
	return c.inner.Stat(name)
}

// MkdirAll implements SandboxFS.
func (c *ChangeTracker) MkdirAll(path string, perm stdfs.FileMode) error {
	return c.inner.MkdirAll(path, perm)
}

// Remove implements SandboxFS.
func (c *ChangeTracker) Remove(name string) error {
	if err := c.inner.Remove(name); err != nil {
		return err
	}
	c.recordDelete(name)
	return nil
}

// RemoveAll implements SandboxFS.
func (c *ChangeTracker) RemoveAll(path string) error {
	if err := c.inner.RemoveAll(path); err != nil {
		return err
	}
	c.recordDelete(path)
	return nil
}

// Rename implements SandboxFS.
func (c *ChangeTracker) Rename(oldpath, newpath string) error {
	if err := c.inner.Rename(oldpath, newpath); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.created, oldpath)
	delete(c.modified, oldpath)
	c.deleted[oldpath] = true
	c.created[newpath] = true
	return nil
}

// ReadFile implements SandboxFS.
func (c *ChangeTracker) ReadFile(name string) ([]byte, error) {
	return c.inner.ReadFile(name)
}

// WriteFile implements SandboxFS.
func (c *ChangeTracker) WriteFile(name string, data []byte, perm stdfs.FileMode) error {
	c.recordWrite(name)
	return c.inner.WriteFile(name, data, perm)
}

// ReadDir implements SandboxFS.
func (c *ChangeTracker) ReadDir(name string) ([]stdfs.DirEntry, error) {
	return c.inner.ReadDir(name)
}
