package sbfs

import (
	"context"
	stdfs "io/fs"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

// NewOpenHandler returns an interp.OpenHandlerFunc-compatible function that
// routes shell file I/O (redirections, here-docs) through sfs.
// sandboxRoot is the absolute real path the sandbox uses as its root.
//
// Relative paths are resolved against the shell runner's working directory via
// interp.HandlerCtx, matching the behaviour of mvdan.cc/sh's DefaultOpenHandler.
//
// Routing rules:
//  1. Paths already within sandboxRoot → routed through sfs as-is.
//  2. Device allowlist (/dev/null, /dev/urandom, /dev/random, /proc/self/fd/*) →
//     passed through to the real OS filesystem.
//  3. All other absolute paths → treated as virtual (e.g. /etc/hostname) and
//     translated to their real counterpart under sandboxRoot. Writes go to sfs;
//     reads also go to sfs (returning ENOENT if not bootstrapped).
func NewOpenHandler(sfs SandboxFS, sandboxRoot string) func(ctx context.Context, path string, flag int, perm stdfs.FileMode) (io.ReadWriteCloser, error) {
	root := filepath.Clean(sandboxRoot)

	return func(ctx context.Context, path string, flag int, perm stdfs.FileMode) (io.ReadWriteCloser, error) {
		// Resolve relative paths against the shell's CWD, exactly as
		// DefaultOpenHandler does.
		if path != "" && !filepath.IsAbs(path) {
			mc := interp.HandlerCtx(ctx)
			path = filepath.Join(mc.Dir, path)
		}
		path = filepath.Clean(path)

		// Already inside the sandbox root → route through sfs.
		if containedBy(root, path) {
			return sfs.OpenFile(path, flag, perm)
		}

		// Device/proc allowlist: always pass through to the real OS.
		if isHostPassthrough(path) {
			return os.OpenFile(path, flag, perm)
		}

		// All other absolute paths are virtual (e.g. /etc/hostname, /workspace).
		// Translate to the real on-disk path and route through sfs.
		// This prevents cat /etc/hostname returning the real hostname, and makes
		// writes to virtual paths land inside the sandbox.
		realPath := filepath.Join(root, path)
		return sfs.OpenFile(realPath, flag, perm)
	}
}

// isHostPassthrough returns true for paths that must be served from the real
// host filesystem regardless of virtual path translation.
func isHostPassthrough(path string) bool {
	switch path {
	case "/dev/null", "/dev/urandom", "/dev/random", "/dev/zero":
		return true
	}
	// /proc/self/fd/* is needed by some Go stdlib operations.
	if strings.HasPrefix(path, "/proc/self/fd/") {
		return true
	}
	return false
}
