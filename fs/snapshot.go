package sbfs

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Snapshot serialises the contents of lfs.Root() into an in-memory tar
// archive.  The archive can later be passed to Restore to reproduce the exact
// same directory state.
//
// Symlinks are preserved as tar.TypeSymlink entries.  Symlinks whose resolved
// target escapes the sandbox root are skipped to prevent exfiltration.
func Snapshot(lfs *LayeredFS) ([]byte, error) {
	root := lfs.Root()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err := filepath.WalkDir(root, func(path string, d stdfs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil // skip root itself
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		// Handle symlinks: emit a TypeSymlink header after validating the target.
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return nil
			}
			if !symlinkWithinRoot(root, path, target) {
				return nil // skip escaping symlinks silently
			}
			hdr := &tar.Header{
				Typeflag: tar.TypeSymlink,
				Name:     rel,
				Linkname: target,
				ModTime:  info.ModTime(),
				Mode:     int64(info.Mode()),
			}
			if writeErr := tw.WriteHeader(hdr); writeErr != nil {
				return fmt.Errorf("snapshot: write symlink header %s: %w", path, writeErr)
			}
			return nil
		}

		hdr, hdrErr := tar.FileInfoHeader(info, "")
		if hdrErr != nil {
			return fmt.Errorf("snapshot: header for %s: %w", path, hdrErr)
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("snapshot: write header %s: %w", path, err)
		}

		if !info.IsDir() {
			f, openErr := os.Open(path)
			if openErr != nil {
				return fmt.Errorf("snapshot: open %s: %w", path, openErr)
			}
			defer f.Close()
			if _, copyErr := io.Copy(tw, f); copyErr != nil {
				return fmt.Errorf("snapshot: copy %s: %w", path, copyErr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("snapshot: close tar: %w", err)
	}
	return buf.Bytes(), nil
}

// Restore wipes the contents of lfs.Root() and repopulates it from the tar
// archive produced by a previous Snapshot call.
func Restore(lfs *LayeredFS, data []byte) error {
	root := lfs.Root()

	// Remove all files under root (but keep root itself).
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("restore: read root: %w", err)
	}
	for _, e := range entries {
		if rmErr := os.RemoveAll(filepath.Join(root, e.Name())); rmErr != nil {
			return fmt.Errorf("restore: remove %s: %w", e.Name(), rmErr)
		}
	}

	// Re-populate from tar.
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("restore: read tar: %w", err)
		}

		target := filepath.Join(root, hdr.Name)
		// Zip-slip protection: reject tar entries that escape the sandbox root.
		rel, relErr := filepath.Rel(root, target)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("restore: path traversal in snapshot entry %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if mkErr := os.MkdirAll(target, hdr.FileInfo().Mode()); mkErr != nil {
				return fmt.Errorf("restore: mkdir %s: %w", target, mkErr)
			}

		case tar.TypeSymlink:
			if !symlinkWithinRoot(root, target, hdr.Linkname) {
				return fmt.Errorf("restore: symlink target escapes sandbox in entry %q", hdr.Name)
			}
			if dir := filepath.Dir(target); dir != "" {
				if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
					return fmt.Errorf("restore: mkdir parent %s: %w", dir, mkErr)
				}
			}
			_ = os.Remove(target) // replace any existing entry
			if linkErr := os.Symlink(hdr.Linkname, target); linkErr != nil {
				return fmt.Errorf("restore: create symlink %s: %w", target, linkErr)
			}

		default: // regular file
			if dir := filepath.Dir(target); dir != "" {
				if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
					return fmt.Errorf("restore: mkdir parent %s: %w", dir, mkErr)
				}
			}
			f, openErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
			if openErr != nil {
				return fmt.Errorf("restore: create %s: %w", target, openErr)
			}
			if _, copyErr := io.Copy(f, tr); copyErr != nil {
				f.Close()
				return fmt.Errorf("restore: write %s: %w", target, copyErr)
			}
			f.Close()
		}
	}
	return nil
}

// symlinkWithinRoot reports whether a symlink's resolved target stays within
// root.  symlinkPath is the absolute real path of the symlink; target is the
// Linkname string (may be absolute or relative).
func symlinkWithinRoot(root, symlinkPath, target string) bool {
	resolved := target
	if !filepath.IsAbs(target) {
		resolved = filepath.Join(filepath.Dir(symlinkPath), target)
	}
	resolved = filepath.Clean(resolved)
	clean := filepath.Clean(root)
	return resolved == clean || strings.HasPrefix(resolved, clean+string(filepath.Separator))
}
