package sbfs

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Snapshot serialises the contents of lfs.Root() into an in-memory tar
// archive.  The archive can later be passed to Restore to reproduce the exact
// same directory state.
func Snapshot(lfs *LayeredFS) ([]byte, error) {
	root := lfs.Root()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
		info := hdr.FileInfo()

		if info.IsDir() {
			if mkErr := os.MkdirAll(target, info.Mode()); mkErr != nil {
				return fmt.Errorf("restore: mkdir %s: %w", target, mkErr)
			}
			continue
		}

		if dir := filepath.Dir(target); dir != "" {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				return fmt.Errorf("restore: mkdir parent %s: %w", dir, mkErr)
			}
		}

		f, openErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if openErr != nil {
			return fmt.Errorf("restore: create %s: %w", target, openErr)
		}
		if _, copyErr := io.Copy(f, tr); copyErr != nil {
			f.Close()
			return fmt.Errorf("restore: write %s: %w", target, copyErr)
		}
		f.Close()
	}
	return nil
}
