package packages

import (
	"os"
	"path/filepath"
	"sync"
)

// DefaultCacheDir returns the shared on-host package cache directory.
// Packages downloaded here are reused across sandboxes on the same host.
func DefaultCacheDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "agentic-bash", "packages")
	}
	return filepath.Join(os.TempDir(), "agentic-bash", "packages")
}

// dirLocks is a two-level lock: the outer mutex protects the map; each
// per-path mutex serialises operations on that specific cache directory.
var dirLocks struct {
	sync.Mutex
	m map[string]*sync.Mutex
}

func init() { dirLocks.m = make(map[string]*sync.Mutex) }

// lockDir acquires the per-directory lock for path and returns the unlock
// function.  Callers should defer the returned function.
func lockDir(path string) func() {
	dirLocks.Lock()
	mu, ok := dirLocks.m[path]
	if !ok {
		mu = &sync.Mutex{}
		dirLocks.m[path] = mu
	}
	dirLocks.Unlock() // release map lock before acquiring per-dir lock to avoid deadlock
	mu.Lock()
	return mu.Unlock
}
