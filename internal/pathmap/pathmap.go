// Package pathmap provides helpers for translating between the virtual paths
// an AI agent sees (e.g. /home/user, /workspace) and the real on-disk paths
// inside the sandbox temp directory (e.g. /tmp/agentic-bash-abc/home/user).
package pathmap

import (
	"path/filepath"
	"strings"
)

// VirtualToReal converts a virtual absolute path (e.g. /home/user/foo) to its
// real on-disk counterpart ({sandboxRoot}/home/user/foo).
// Paths already under sandboxRoot are returned as-is.
func VirtualToReal(sandboxRoot, virtual string) string {
	clean := filepath.Clean(virtual)
	root := filepath.Clean(sandboxRoot)
	if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return clean
	}
	return filepath.Join(root, clean)
}

// RealToVirtual converts a real on-disk path back to a virtual path by
// stripping the sandboxRoot prefix.
// If real is not under sandboxRoot, it is returned as-is.
func RealToVirtual(sandboxRoot, real string) string {
	clean := filepath.Clean(real)
	root := filepath.Clean(sandboxRoot)
	if clean == root {
		return "/"
	}
	rel, err := filepath.Rel(root, clean)
	if err != nil || strings.HasPrefix(rel, "..") {
		return clean
	}
	return "/" + rel
}

// IsEscaping reports whether real resolves outside sandboxRoot (path traversal).
func IsEscaping(sandboxRoot, real string) bool {
	clean := filepath.Clean(real)
	root := filepath.Clean(sandboxRoot)
	if clean == root {
		return false
	}
	return !strings.HasPrefix(clean, root+string(filepath.Separator))
}
