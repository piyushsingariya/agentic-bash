package sbfs

import (
	"context"
	stdfs "io/fs"
	"io"
	"os"
	"path/filepath"

	"mvdan.cc/sh/v3/interp"
)

// NewOpenHandler returns an interp.OpenHandlerFunc-compatible function that
// routes shell file I/O (redirections, here-docs) through sfs.
// sandboxRoot is the absolute path the sandbox considers its root.
//
// Relative paths are resolved against the shell runner's working directory via
// interp.HandlerCtx, matching the behaviour of mvdan.cc/sh's DefaultOpenHandler.
//
// Routing rules:
//   - Paths within sandboxRoot  → always routed through sfs.
//   - Paths outside sandboxRoot → reads fall through to the real OS filesystem;
//     writes are rejected with stdfs.ErrPermission.
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

		if containedBy(root, path) {
			return sfs.OpenFile(path, flag, perm)
		}

		// /dev/null is always allowed regardless of direction.
		if path == "/dev/null" {
			return os.OpenFile(path, flag, perm)
		}

		// Paths outside the sandbox root: writes are blocked, reads fall through.
		if isWriteFlag(flag) {
			// Return *os.PathError so mvdan.cc/sh treats it as a non-fatal redirect
			// error (prints to stderr) rather than aborting the whole script.
			return nil, &os.PathError{Op: "open", Path: path, Err: stdfs.ErrPermission}
		}

		// Allow reads from the host filesystem (e.g. /etc/hostname, /usr/share/…).
		return os.OpenFile(path, flag, perm)
	}
}
