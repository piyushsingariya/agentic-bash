# Plan: Embedded Base Image (`Options.BaseImageDir`)

## Goal

Allow users to supply a pre-baked directory (or an embedded tar.gz archive) that
forms the **read-only lower layer** of every sandbox filesystem. This is how
Docker base images work, but without Docker: an overlay of user-writable tmpdir
on top of a static read-only root.

Two sub-goals:
1. **Runtime `BaseImageDir`** — use a host directory path as the lower layer.
   Already accepted by `Options.BaseImageDir`; the field exists but the
   implementation is a no-op stub. This plan wires it up.
2. **Embedded `//go:embed` base archive** — bundle a minimal rootfs directly
   inside the binary so sandboxes get a useful default tool set without any
   host configuration.

---

## Current state

`Options.BaseImageDir` is declared in `sandbox/options.go` but never read.
The `OsFS` implementation in `fs/osfs.go` has no concept of a lower layer;
all reads and writes go to `root` (the temp directory).

---

## Files to create / modify

| Path | Action |
|---|---|
| `fs/layered.go` | New — `LayeredFS` that overlays a writable upper dir over a read-only lower dir |
| `fs/base/base.tar.gz` | New — minimal embedded rootfs archive (optional, see §Embedded base) |
| `fs/base/embed.go` | New — `//go:embed base.tar.gz`; `Extract(dst string) error` helper |
| `sandbox/sandbox.go` | Modify — unpack `BaseImageDir` (or embedded base) into lower layer; construct `LayeredFS` |
| `fs/osfs.go` | Possibly extend — currently handles all path operations; `LayeredFS` wraps it |

---

## `fs/layered.go` — `LayeredFS`

```go
package fs

import (
    "io/fs"
    "os"
    "path/filepath"
    "strings"
)

// LayeredFS presents a unified view of an upper (writable) directory over a
// lower (read-only) directory. Writes always go to upper. Reads check upper
// first, then fall back to lower.
//
// This is analogous to overlayfs but implemented entirely in userspace via
// the existing OsFS path-checking logic.
type LayeredFS struct {
    upper *OsFS // writable; sandbox temp dir
    lower string // read-only base image dir (may be "")
}

// NewLayeredFS creates a LayeredFS. lower may be empty (disables lower layer).
func NewLayeredFS(upper *OsFS, lower string) *LayeredFS {
    return &LayeredFS{upper: upper, lower: lower}
}

// resolvePath returns the concrete path for reading: upper if it exists there,
// then lower. Writes always go to upper via upper.resolvePath.
func (l *LayeredFS) readPath(rel string) string {
    up := l.upper.resolvePath(rel)
    if _, err := os.Lstat(up); err == nil {
        return up // exists in upper layer
    }
    if l.lower == "" {
        return up // no lower layer; return upper path (may not exist)
    }
    low := filepath.Join(l.lower, filepath.Clean("/"+rel))
    if !strings.HasPrefix(low, l.lower) {
        return up // path traversal attempt; fall back to upper
    }
    return low
}

// Implement the fs.FS, WriteFile, ReadFile, ReadDir, Stat, MkdirAll, Remove,
// Rename, and Symlink interfaces by delegating to upper for writes and
// readPath() for reads.
```

The key principle: **all mutations go to upper; reads fall through to lower**.
This means deleting a lower-layer file only "shadows" it (it remains in lower)
unless we implement whiteout files like overlayfs does. For the initial
implementation, skip whiteouts — any file in lower is readable unless the upper
has a version.

### Interface parity with `OsFS`

`LayeredFS` must satisfy the same interface that `sandbox.go` uses today for
`s.fs`. Inspect `sandbox/sandbox.go` for which methods are called, and implement
the same set forwarding writes to `upper` and reads through `readPath`.

---

## `sandbox/sandbox.go` changes

In `New()`, after creating the temp directory:

```go
var lowerDir string
if opts.BaseImageDir != "" {
    lowerDir = opts.BaseImageDir
} else if base.Available() {
    // Unpack the embedded base archive once per process into a shared dir.
    lowerDir, err = base.EnsureExtracted()
    if err != nil {
        return nil, fmt.Errorf("extract embedded base: %w", err)
    }
}

var fsys SandboxFS
if lowerDir != "" {
    fsys = fs.NewLayeredFS(fs.NewOsFS(tmpDir), lowerDir)
} else {
    fsys = fs.NewOsFS(tmpDir)
}
```

---

## Embedded base archive (`fs/base/`)

### `fs/base/embed.go`

```go
//go:build !nobase

package base

import (
    _ "embed"
    "os"
    "path/filepath"
    "sync"
    "archive/tar"
    "compress/gzip"
    "io"
    "strings"
)

//go:embed base.tar.gz
var baseArchive []byte

var (
    extractOnce sync.Once
    extractDir  string
    extractErr  error
)

// Available reports whether a built-in base archive is included.
func Available() bool { return len(baseArchive) > 0 }

// EnsureExtracted unpacks the embedded base.tar.gz into a shared temp
// directory the first time it is called. Subsequent calls return the
// same directory.
func EnsureExtracted() (string, error) {
    extractOnce.Do(func() {
        dir, err := os.MkdirTemp("", "agentic-bash-base-*")
        if err != nil {
            extractErr = err
            return
        }
        if err := extractTar(bytes.NewReader(baseArchive), dir); err != nil {
            _ = os.RemoveAll(dir)
            extractErr = err
            return
        }
        extractDir = dir
    })
    return extractDir, extractErr
}
```

Provide a `//go:build nobase` stub in `fs/base/embed_stub.go` for builds where
embedding is not desired (CI, lightweight deployments):

```go
//go:build nobase

package base

func Available() bool          { return false }
func EnsureExtracted() (string, error) { return "", nil }
```

### What to put in `base.tar.gz`

A minimal `/usr/bin` with commonly needed tools. Options:

**Option A — Busybox static binary**: Single binary providing `sh`, `ls`, `cat`,
`grep`, `sed`, `awk`, `find`, `curl`, `tar`, `gzip`, `wget`, etc. ~2 MiB.

**Option B — Alpine minirootfs**: Full Alpine Linux base (~3 MiB compressed).
Includes `/etc/passwd`, `/etc/group`, `/etc/ssl/certs`, apk, and common tools.

**Option C — Custom minimal set**: Hand-pick a tiny set of static binaries
compiled for Linux/amd64 and Linux/arm64, multi-arch fat archive.

For the initial implementation, **Option A (Busybox)** is recommended. It gives
the widest tool coverage per byte. A `Makefile` target or `scripts/fetch-base.sh`
should download and repackage it at build time:

```bash
#!/usr/bin/env bash
# scripts/fetch-base.sh
# Downloads busybox static binary and creates fs/base/base.tar.gz
set -euo pipefail
BUSYBOX_URL="https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox"
TMP=$(mktemp -d)
curl -fsSL "$BUSYBOX_URL" -o "$TMP/busybox"
chmod +x "$TMP/busybox"
# Install all applets as symlinks
"$TMP/busybox" --list | while read applet; do
    ln -sf busybox "$TMP/$applet"
done
tar czf fs/base/base.tar.gz -C "$TMP" .
rm -rf "$TMP"
```

`base.tar.gz` is committed to the repo (or downloaded by CI via `go generate`).

---

## ChangeTracker and lower layer

The existing `ChangeTracker` (wraps `OsFS`) records writes to the upper layer.
Lower-layer files that are read-only should not appear in `FilesCreated` /
`FilesModified`. Since `ChangeTracker` only intercepts writes going to the upper
`OsFS`, no changes are needed.

---

## Snapshot/Restore interaction

`sbfs.Snapshot` and `sbfs.Restore` today snapshot only the upper layer (the
writable temp dir). With `LayeredFS`, the contract is the same: snapshots
capture only the user-writable delta, not the base image. `EnsureExtracted()`
will re-create the lower layer on restore without re-snapshotting it.

---

## Build tags

| Tag | Effect |
|---|---|
| _(default)_ | `base.tar.gz` is embedded; full base layer available |
| `nobase` | Stub no-op; binary is smaller, no pre-baked tools |

CI matrix should test both configurations.

---

## Tests

```go
func TestBaseLayerToolsAvailable(t *testing.T) {
    if !base.Available() {
        t.Skip("built with -tags nobase")
    }
    sb := newSandbox(t, sandbox.Options{}) // uses embedded base
    r := mustRun(t, sb, `busybox --help 2>&1 | head -1`)
    if !strings.Contains(r.Stdout+r.Stderr, "BusyBox") {
        t.Error("busybox not available in sandbox")
    }
}

func TestBaseLayerOverriddenByUpper(t *testing.T) {
    if !base.Available() {
        t.Skip("built with -tags nobase")
    }
    sb := newSandbox(t, sandbox.Options{})
    root := sb.State().Cwd
    // Write a file that shadows one from the base layer.
    _ = sb.WriteFile(root+"/usr/bin/ls", []byte("#!/bin/sh\necho overridden"))
    r := mustRun(t, sb, `ls`)
    if !strings.Contains(r.Stdout, "overridden") {
        t.Error("upper layer did not shadow base layer")
    }
}

func TestCustomBaseImageDir(t *testing.T) {
    dir := t.TempDir()
    _ = os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("from-base"), 0o644)
    sb := newSandbox(t, sandbox.Options{BaseImageDir: dir})
    r := mustRun(t, sb, `cat /hello.txt`)
    if strings.TrimSpace(r.Stdout) != "from-base" {
        t.Errorf("base file not readable; got %q", r.Stdout)
    }
}
```

---

## Key design decisions

1. **Userspace overlay, not kernel overlayfs**: Kernel overlayfs requires root
   and a `mount` syscall. The pure-Go read-through approach achieves the same
   semantic for the use cases that matter (tool availability, common configs)
   without any privileges.

2. **Shared lower layer**: `EnsureExtracted()` extracts once per process into a
   shared read-only directory. All sandboxes in the same process share the same
   lower layer, saving disk I/O and memory.

3. **No whiteouts for MVP**: Deleting a base-layer file is not supported in v1.
   If a user tries to `rm /usr/bin/ls`, they'll remove the upper-layer shadow
   (if any) but the lower-layer file remains readable. This can be addressed
   with whiteout marker files (e.g., `.wh.<name>`) in a follow-up.

4. **`nobase` build tag for production embedding control**: Operators who provide
   their own tooling via `BaseImageDir` don't need the embedded archive.
