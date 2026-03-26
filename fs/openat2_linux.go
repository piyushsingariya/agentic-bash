//go:build linux

package sbfs

import (
	"errors"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	openat2Once      sync.Once
	openat2Supported bool
)

// probeOpenat2 checks once whether the kernel supports openat2(2).
// Kernels older than 5.6 return ENOSYS; anything else means the syscall exists.
func probeOpenat2() bool {
	openat2Once.Do(func() {
		how := unix.OpenHow{
			Flags:   unix.O_RDONLY,
			Resolve: unix.RESOLVE_IN_ROOT,
		}
		// dirfd=-1 and path="" will return EBADF or EINVAL on supported kernels,
		// ENOSYS on unsupported ones.
		fd, err := unix.Openat2(-1, "", &how)
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		openat2Supported = !errors.Is(err, syscall.ENOSYS)
	})
	return openat2Supported
}

// openat2InRoot opens path using openat2(2) with RESOLVE_IN_ROOT so the kernel
// resolves all symlinks atomically within rootDir, preventing TOCTOU races.
// Returns errors.ErrUnsupported when the syscall is not available (kernel < 5.6
// or non-Linux build), so callers can fall back to afero.OsFs.
func openat2InRoot(rootDir, path string, flag int, perm stdfs.FileMode) (*os.File, error) {
	if !probeOpenat2() {
		return nil, errors.ErrUnsupported
	}

	rootFD, err := unix.Open(rootDir, unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)

	// Make path relative to rootDir for openat2.
	rel := strings.TrimPrefix(filepath.Clean(path), filepath.Clean(rootDir))
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	if rel == "" {
		rel = "."
	}

	how := unix.OpenHow{
		Flags:   uint64(flag),
		Mode:    uint64(perm.Perm()),
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS,
	}

	fd, err := unix.Openat2(rootFD, rel, &how)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
