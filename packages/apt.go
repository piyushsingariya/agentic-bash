package packages

import (
	"archive/tar"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

// AptShim intercepts apt-get / apt install commands and extracts packages
// directly into the sandbox overlay without touching the host package database.
type AptShim struct {
	cfg ShimConfig
}

// Matches reports whether args is an apt / apt-get invocation for a supported
// subcommand (install, remove, purge, update, upgrade).
func (s *AptShim) Matches(args []string) bool {
	if len(args) < 2 {
		return false
	}
	base := filepath.Base(args[0])
	if base != "apt" && base != "apt-get" {
		return false
	}
	switch args[1] {
	case "install", "remove", "purge", "update", "upgrade":
		return true
	}
	return false
}

func (s *AptShim) handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	switch args[1] {
	case "install":
		return s.install(ctx, hc, args[2:])
	case "remove", "purge":
		return s.remove(hc, args[2:])
	case "update", "upgrade":
		fmt.Fprintln(hc.Stderr, "apt: update/upgrade are no-ops inside the sandbox overlay")
		return nil
	}
	return nil
}

func (s *AptShim) install(ctx context.Context, hc interp.HandlerContext, pkgArgs []string) error {
	// Parse package names; skip flags like -y, --no-install-recommends.
	var pkgs []string
	for _, a := range pkgArgs {
		if !strings.HasPrefix(a, "-") {
			pkgs = append(pkgs, a)
		}
	}
	if len(pkgs) == 0 {
		return nil
	}

	// apt-get must be available on the host.
	aptBin, err := lookBin(hc, "apt-get")
	if err != nil {
		return fmt.Errorf("apt-get not found — cannot install packages in sandbox mode on this host")
	}

	archivesDir := filepath.Join(s.cfg.CacheDir, "apt", "archives")
	if err := ensureDir(archivesDir); err != nil {
		return fmt.Errorf("apt: create cache dir: %w", err)
	}

	// Serialize per-cache-dir to avoid concurrent downloads of the same package.
	unlock := lockDir(archivesDir)
	defer unlock()

	// Download packages (--download-only leaves them in archivesDir).
	dlArgs := []string{
		aptBin, "install",
		"-y", "--no-install-recommends", "--download-only",
		"-o", "Dir::Cache=" + filepath.Join(s.cfg.CacheDir, "apt"),
		"-o", "Dir::Cache::archives=" + archivesDir,
	}
	dlArgs = append(dlArgs, pkgs...)

	dlCmd := exec.CommandContext(ctx, dlArgs[0], dlArgs[1:]...)
	dlCmd.Stdout = hc.Stdout
	dlCmd.Stderr = hc.Stderr
	if err := dlCmd.Run(); err != nil {
		return toExitErr(err)
	}

	// Extract every .deb in the archive dir into the overlay root.
	debs, _ := filepath.Glob(filepath.Join(archivesDir, "*.deb"))
	for _, deb := range debs {
		if err := extractDeb(deb, s.cfg.OverlayRoot); err != nil {
			fmt.Fprintf(hc.Stderr, "apt: warning: extract %s: %v\n", filepath.Base(deb), err)
		}
	}

	if s.cfg.Manifest != nil {
		for _, pkg := range pkgs {
			s.cfg.Manifest.Record(PackageInfo{Name: pkg, Manager: "apt"})
		}
	}
	return nil
}

func (s *AptShim) remove(hc interp.HandlerContext, pkgArgs []string) error {
	for _, a := range pkgArgs {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if s.cfg.Manifest != nil {
			s.cfg.Manifest.Remove(a, "apt")
		}
		fmt.Fprintf(hc.Stderr, "apt: removed %s from manifest (overlay files not cleaned)\n", a)
	}
	return nil
}

// ── .deb extraction ──────────────────────────────────────────────────────────

// extractDeb extracts the data section of a .deb file into destRoot.
// It tries dpkg-deb first (fast, handles all compression formats) and falls
// back to a pure-Go ar + tar extractor for systems without dpkg-deb.
func extractDeb(debPath, destRoot string) error {
	if dpkg, err := exec.LookPath("dpkg-deb"); err == nil {
		cmd := exec.Command(dpkg, "-x", debPath, destRoot)
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return extractDebPureGo(debPath, destRoot)
}

// extractDebPureGo implements a minimal ar archive reader that finds the
// data.tar.* member inside a .deb and unpacks it into destRoot.
func extractDebPureGo(debPath, destRoot string) error {
	f, err := os.Open(debPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// ar global header: exactly "!<arch>\n" (8 bytes).
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "!<arch>\n" {
		return fmt.Errorf("not a valid ar archive: %s", filepath.Base(debPath))
	}

	for {
		// Each ar member starts with a 60-byte header.
		var hdr [60]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			break // EOF or truncated — either way, done
		}
		if string(hdr[58:60]) != "`\n" {
			return fmt.Errorf("invalid ar member header in %s", filepath.Base(debPath))
		}

		name := strings.TrimRight(string(hdr[:16]), " ")
		sizeStr := strings.TrimRight(string(hdr[48:58]), " ")
		size, _ := strconv.ParseInt(sizeStr, 10, 64)

		lr := io.LimitReader(f, size)

		if strings.HasPrefix(name, "data.tar") {
			if err := extractTarSection(lr, name, destRoot); err != nil {
				return fmt.Errorf("extract %s from %s: %w", name, filepath.Base(debPath), err)
			}
		} else {
			if _, err := io.Copy(io.Discard, lr); err != nil {
				return err
			}
		}

		// ar members are padded to even byte boundaries.
		if size%2 != 0 {
			if _, err := f.Read(make([]byte, 1)); err != nil {
				break
			}
		}
	}
	return nil
}

// extractTarSection decompresses and extracts a tar archive. The name
// parameter (e.g. "data.tar.gz") is used to select the decompressor.
func extractTarSection(r io.Reader, name, destRoot string) error {
	var tr *tar.Reader

	switch {
	case strings.HasSuffix(name, ".gz"):
		gr, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer gr.Close()
		tr = tar.NewReader(gr)

	case strings.HasSuffix(name, ".bz2"):
		tr = tar.NewReader(bzip2.NewReader(r))

	case strings.HasSuffix(name, ".xz"):
		// xz requires the host xz binary; read all data then decompress.
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		decompressed, err := decompressXZ(data)
		if err != nil {
			return err
		}
		tr = tar.NewReader(bytes.NewReader(decompressed))

	case strings.HasSuffix(name, ".zst"):
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		decompressed, err := decompressZstd(data)
		if err != nil {
			return err
		}
		tr = tar.NewReader(bytes.NewReader(decompressed))

	default:
		// Assume uncompressed tar.
		tr = tar.NewReader(r)
	}

	return extractTarEntries(tr, destRoot)
}

// extractTarEntries writes tar entries to destRoot, sanitising paths.
func extractTarEntries(tr *tar.Reader, destRoot string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Sanitise the entry path.
		name := filepath.Clean(hdr.Name)
		name = strings.TrimPrefix(name, "./")
		name = strings.TrimPrefix(name, "/")
		if name == "" || name == "." || strings.Contains(name, "..") {
			continue
		}

		dest := filepath.Join(destRoot, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, hdr.FileInfo().Mode()|0o100); err != nil {
				return err
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return copyErr
			}

		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dest)
			_ = os.Symlink(hdr.Linkname, dest) // non-fatal: target may not exist yet
		}
	}
	return nil
}

// decompressXZ decompresses xz-compressed data using the host xz binary.
func decompressXZ(data []byte) ([]byte, error) {
	cmd := exec.Command("xz", "-d", "-c")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("xz decompression requires 'xz' binary on host: %w", err)
	}
	return out, nil
}

// decompressZstd decompresses zstd-compressed data using the host zstd binary.
func decompressZstd(data []byte) ([]byte, error) {
	cmd := exec.Command("zstd", "-d", "-c")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("zstd decompression requires 'zstd' binary on host: %w", err)
	}
	return out, nil
}
