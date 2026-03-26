# Plan: Symlink Sandbox Escape Fix (Issue #1)

## Goal

Close the symlink-based containment bypass that allows arbitrary host file reads
even with Landlock LSM active. Implement a layered, defence-in-depth fix across
the filesystem, executor intercept, isolation, and tar-handling layers.

---

## Background

`checkContainment()` in `fs/fs.go` uses `filepath.Clean()` which normalises `..`
sequences but **does not resolve symlinks**. A symlink created inside the sandbox
(`ln -s /etc/passwd /sandbox/link`) passes the check because the symlink's own
path is within root; `afero.OsFs.OpenFile()` then follows the symlink to the
real host path.

The bypass works even with Landlock because `/etc`, `/usr`, `/bin` etc. are
explicitly in the current Landlock allowlist (`isolation/landlock_linux.go`).

A simple `filepath.EvalSymlinks()` hotfix was considered but rejected for
long-term use because:
- It has a TOCTOU race (check and open are not atomic).
- It does not prevent symlink *creation* to outside targets.
- It does not fix the Landlock misconfiguration.
- It does not protect `DownloadTar` / `Snapshot` from walking into symlinked
  host directories.

---

## Phases

### Phase 1 — Intercept `ln` + add `SandboxFS.Symlink`
**Priority: highest — do first; unblocks all other phases**
**Platforms: all (macOS + Linux)**

`ln -s` reaches `IsolatedExecHandler` and spawns the real host `ln` binary.
There is no interceptor for it in the `Dispatcher` registry. This must be
closed before any other fix matters.

**Files to modify / create:**

| File | Change |
|------|--------|
| `fs/fs.go` | Add `Symlink(oldname, newname string) error` to `SandboxFS` interface; add `checkSymlinkTarget(root, target string) error` helper that validates an absolute or relative-resolved target stays within root |
| `fs/realfs.go` | Implement `OsFS.Symlink()`: check `newname` is within root, resolve target relative to `filepath.Dir(newname)` if relative, check resolved target is within root, then call `os.Symlink` |
| `fs/memory.go` | Implement `MemoryFS.Symlink()` returning `syscall.ENOTSUP` — in-memory FS has no real symlinks |
| `fs/tracker.go` | Add `Symlink` pass-through to `ChangeTracker`, recording `newname` as a created file |
| `executor/intercept/filesystem.go` | Add `lnInterceptor` struct implementing `Interceptor`; parses `ln [-s] [-f] oldname newname`, calls `SandboxFS.Symlink` for `-s` and validates both paths for hard links; needs a `FS SandboxFS` field on `Config` or a callback |
| `sandbox/sandbox.go` | Register `lnInterceptor` in `wireHandlers()`; pass `SandboxFS` reference into intercept `Config` |

**What this covers:**
- Stops `ln -s /etc/passwd /sandbox/link` at the command dispatch layer.
- Validates both the link path and the target before any kernel call.

**What this does NOT cover:**
- Subprocesses calling `os.symlink()` from Python, Perl, Ruby, etc. directly —
  those bypass the Go handler chain entirely. Only Phase 4 (chroot) fully
  closes that vector.

---

### Phase 2 — `openat2(2)` with `RESOLVE_IN_ROOT`
**Priority: high**
**Platforms: Linux 5.6+ (graceful fallback on older kernels and macOS)**

Even with Phase 1, a TOCTOU race remains: create symlink inside sandbox →
validate target → before `os.OpenFile`, race to swap the target to point
outside. `openat2(2)` with `RESOLVE_IN_ROOT` makes the kernel resolve all
symlinks atomically within a root file descriptor, with no userspace check
between resolution and open.

`golang.org/x/sys` is already a dependency; `unix.Openat2` and
`unix.RESOLVE_IN_ROOT` are available without new dependencies.

**Files to create / modify:**

| File | Change |
|------|--------|
| `fs/openat2_linux.go` *(new)* | `openat2InRoot(rootDir, path string, flags int, perm fs.FileMode) (*os.File, error)` — opens `rootDir` as fd, computes a relative path, calls `unix.Openat2` with `RESOLVE_IN_ROOT \| RESOLVE_NO_MAGICLINKS`; returns `syscall.ENOSYS` on kernels < 5.6 |
| `fs/openat2_other.go` *(new)* | Stub returning `errors.ErrUnsupported` (macOS / non-Linux) |
| `fs/realfs.go` | `OsFS.OpenFile()` attempts `openat2InRoot` first; on `ENOSYS` or `ErrUnsupported` falls back to `afero.OsFs`; probe result cached via `sync.Once` to avoid repeated syscall overhead |

**Kernel behaviour with `RESOLVE_IN_ROOT`:**
- All symlinks (including `..`) are resolved within the root fd's filesystem
  subtree.
- Even a symlink `link -> ../../../etc/passwd` resolves to
  `{rootfd}/etc/passwd`, not host `/etc/passwd`.
- `RESOLVE_NO_MAGICLINKS` additionally blocks `/proc/self/exe`-style magic
  symlinks.

**Degradation model:**
- Linux 5.6+: full TOCTOU-free protection.
- Linux < 5.6 / macOS: path-validation only (Phase 1 + `checkContainment`).

---

### Phase 3 — Tighten the Landlock allowlist
**Priority: medium**
**Platforms: Linux only (`isolation/landlock_linux.go`)**

The current `Apply()` hardcodes `/etc`, `/usr`, `/lib`, `/lib64`, `/bin`,
`/tmp` as globally accessible. Any Landlock policy that intends to confine
the sandbox to `tempDir` is undermined because a symlink to `/etc/passwd` is
explicitly permitted by the LSM ruleset.

**Files to modify:**

| File | Change |
|------|--------|
| `isolation/landlock_linux.go` | Remove hardcoded broad allowlist from `Apply()`; only add `/proc` (read-only) and `/dev` (read-only, char devices) as runtime essentials; add the sandbox `tempDir` as the sole fully-writable path; expose `NewLandlockStrategy(sandboxRoot string) *LandlockStrategy` so the root is passed in |
| `sandbox/sandbox.go` | Call `isolation.NewLandlockStrategy(tempDir)` directly when `IsolationAuto` or `IsolationLandlock` is selected, instead of delegating to `SelectStrategy()` |

**Note on current call sites:**
`Apply()` is never called in the default subprocess flow (only `Wrap()`, which
is a no-op for Landlock). The impact on existing users is therefore minimal.
This fix matters for whole-process sandboxing mode and prevents future
regressions when `Apply()` is wired in.

---

### Phase 4 — Mount namespace + `chroot` (opt-in)
**Priority: medium — requires design decision**
**Platforms: Linux only**

With `SysProcAttr.Chroot = sandboxRoot` + `CLONE_NEWNS`, subprocesses see
`sandboxRoot` as `/`. A symlink `link -> /etc/passwd` resolves to
`{sandboxRoot}/etc/passwd` — the sandbox's own synthetic file — because there
is no host root visible inside the chroot. This is the **only** fix that closes
the subprocess-level vector (Python `os.symlink`, Perl, etc.).

**Files to modify:**

| File | Change |
|------|--------|
| `isolation/namespace_linux.go` | Add `sandboxRoot string` field to `NamespaceStrategy`; in `Wrap()`, set `cmd.SysProcAttr.Chroot = sandboxRoot` and `cmd.Dir = "/"` when `sandboxRoot` is non-empty |
| `sandbox/sandbox.go` | Pass `tempDir` to `NamespaceStrategy` constructor |
| `sandbox/options.go` (or `sandbox.go`) | Add `StrictNamespace bool` option; `Chroot` is only set when this is true, since it requires a fully-populated `BaseImageDir` to work (binaries must exist inside the sandbox root) |

**Caveat:** With `Chroot`, all external commands (python, git, curl, etc.) must
exist inside the sandbox root as real executables. Without a populated
`BaseImageDir`, every external command fails with ENOENT. Gate behind
`Options.StrictNamespace` and document the requirement.

---

### Phase 5 — Fix `DownloadTar` / `Snapshot` symlink leakage
**Priority: medium**
**Platforms: all**

`DownloadTar` uses `filepath.Walk` which follows symlinks into subdirectories.
A symlink `link -> /etc` causes the walker to descend into host `/etc` and
include its files in the tarball — a data-exfiltration path. `Snapshot` in
`fs/snapshot.go` has the same issue.

**Files to modify:**

| File | Change |
|------|--------|
| `sandbox/sandbox.go` (`DownloadTar`) | Replace `filepath.Walk` with `filepath.WalkDir` + `os.Lstat` on each entry; emit `tar.TypeSymlink` headers for symlinks; validate symlink target is within sandbox root before inclusion; skip or error on escaping targets |
| `fs/snapshot.go` (`Snapshot` + `Restore`) | Use `filepath.WalkDir` with explicit symlink detection via `d.Type()&fs.ModeSymlink`; add `tar.TypeSymlink` case to `Restore` that validates and recreates symlinks via `OsFS.Symlink` |

---

### Phase 6 — Fix `UploadTar` silent symlink ignore
**Priority: low**
**Platforms: all**

`UploadTar` silently drops `tar.TypeSymlink` entries (the switch only handles
directories and regular files). An uploaded archive with a valid symlink entry
should be created; an invalid one (target escapes root) should return an error.

**Files to modify:**

| File | Change |
|------|--------|
| `sandbox/sandbox.go` (`UploadTar`) | Add `tar.TypeSymlink` case: validate `hdr.Linkname` using `checkSymlinkTarget(sandboxRoot, hdr.Linkname)`; call `os.Symlink(hdr.Linkname, target)` on success |

---

## Implementation Order

```
Phase 1 (ln interceptor + SandboxFS.Symlink)     ← start here
    ├── Phase 2 (openat2, TOCTOU-free opens)      ← can run in parallel after Phase 1
    ├── Phase 5 (DownloadTar/Snapshot leakage)    ← independent, any time
    └── Phase 6 (UploadTar silent ignore)         ← independent, any time
Phase 3 (Landlock allowlist tightening)           ← after Phase 1
Phase 4 (Chroot namespace, opt-in)               ← after Phase 3; needs StrictNamespace design
```

---

## Coverage Summary

| Phase | What | Blocks naive ln? | Blocks TOCTOU? | Blocks subprocess exploits? | Blocks exfil via tar? |
|-------|------|:-:|:-:|:-:|:-:|
| 1 | `ln` interceptor + `SandboxFS.Symlink` | **Yes** | No | No | No |
| 2 | `openat2` `RESOLVE_IN_ROOT` | Yes | **Yes** | No | No |
| 3 | Landlock allowlist fix | Partial | No | No | No |
| 4 | Chroot namespace (opt-in) | Yes | Yes | **Yes** | No |
| 5 | `DownloadTar`/`Snapshot` fix | — | — | — | **Yes** |
| 6 | `UploadTar` symlink handling | — | — | — | Partial |

Phases 1 + 2 together give the strongest practical coverage without requiring a
populated filesystem image. Phase 4 is the only complete closure of the
subprocess vector but requires opt-in with a full `BaseImageDir`.
