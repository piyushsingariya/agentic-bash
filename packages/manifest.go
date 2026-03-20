package packages

import (
	"sort"
	"sync"
)

// PackageInfo describes a single package installed into the sandbox overlay.
type PackageInfo struct {
	Name    string // e.g. "requests", "curl"
	Version string // e.g. "2.28.0"; empty when unknown
	Manager string // "pip" or "apt"
}

// manifestKey returns the unique map key for a Name+Manager pair.
func manifestKey(name, manager string) string { return name + "|" + manager }

// Manifest tracks the packages installed during the lifetime of a Sandbox.
// All methods are safe for concurrent use.
type Manifest struct {
	mu      sync.RWMutex
	entries map[string]PackageInfo // keyed by manifestKey
}

// NewManifest creates an empty Manifest.
func NewManifest() *Manifest {
	return &Manifest{entries: make(map[string]PackageInfo)}
}

// Record adds or updates a package entry. If a package with the same
// Name+Manager already exists, its entry is replaced.
func (m *Manifest) Record(info PackageInfo) {
	m.mu.Lock()
	m.entries[manifestKey(info.Name, info.Manager)] = info
	m.mu.Unlock()
}

// Remove deletes a package entry.
func (m *Manifest) Remove(name, manager string) {
	m.mu.Lock()
	delete(m.entries, manifestKey(name, manager))
	m.mu.Unlock()
}

// IsInstalled reports whether a package with the given Name+Manager is recorded.
func (m *Manifest) IsInstalled(name, manager string) bool {
	m.mu.RLock()
	_, ok := m.entries[manifestKey(name, manager)]
	m.mu.RUnlock()
	return ok
}

// Installed returns a sorted snapshot of all recorded entries.
// Sorted by Manager then Name for deterministic test output.
func (m *Manifest) Installed() []PackageInfo {
	m.mu.RLock()
	out := make([]PackageInfo, 0, len(m.entries))
	for _, v := range m.entries {
		out = append(out, v)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Manager != out[j].Manager {
			return out[i].Manager < out[j].Manager
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Clone returns a deep copy suitable for embedding in a filesystem snapshot.
func (m *Manifest) Clone() *Manifest {
	m.mu.RLock()
	c := &Manifest{entries: make(map[string]PackageInfo, len(m.entries))}
	for k, v := range m.entries {
		c.entries[k] = v
	}
	m.mu.RUnlock()
	return c
}

// Restore replaces the manifest's contents with those from src.
func (m *Manifest) Restore(src *Manifest) {
	src.mu.RLock()
	clone := make(map[string]PackageInfo, len(src.entries))
	for k, v := range src.entries {
		clone[k] = v
	}
	src.mu.RUnlock()

	m.mu.Lock()
	m.entries = clone
	m.mu.Unlock()
}
