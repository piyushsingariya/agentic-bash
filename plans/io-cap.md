# Plan: cgroupv2 I/O Bandwidth Cap (`io.max`)

## Goal

Extend the cgroupv2 integration to support per-process I/O bandwidth limits via
the `io.max` control file. This lets operators cap disk read/write throughput of
sandboxed commands, preventing I/O-heavy workloads from starving the host.

---

## Background: cgroupv2 `io.max`

The `io.max` file controls per-device read/write byte-per-second limits:

```
<major>:<minor> rbps=<n> wbps=<n> riops=<n> wiops=<n>
```

Key constraints:
1. Limits are **per block device** — you must know which `<major>:<minor>` pair
   the sandbox tempdir lives on.
2. The kernel only enforces `io.max` on **cgroup v2** with the `io` controller
   enabled (listed in `/sys/fs/cgroup/cgroup.controllers`).
3. Device numbers are read from `stat(2)` — `syscall.Stat_t.Dev`.
4. `rbps=max` and `wbps=max` are the sentinel "unlimited" values.

---

## Files to modify

| Path | Change |
|---|---|
| `internal/cgroups/cgroups.go` | Add `MaxIOReadBytesPerSec`, `MaxIOWriteBytesPerSec` to `Opts`; add `IOPeakReadBytes`, `IOPeakWriteBytes` to `Cgroup.Stop()` return — or extend `Stop()` struct |
| `internal/cgroups/cgroups_linux.go` | Implement `io.max` write in `New()`; harvest `io.stat` in `Stop()` |
| `isolation/exechandler.go` | Thread `MaxIOReadBytesPerSec`/`MaxIOWriteBytesPerSec` from `ExecLimits` into `cgroups.Opts` |
| `sandbox/options.go` | Add `MaxIOReadMBPerSec`, `MaxIOWriteMBPerSec` to `ResourceLimits` |
| `sandbox/sandbox.go` | Wire new limits fields into `ExecLimits` inside `wireHandlers()` |

---

## `internal/cgroups/cgroups.go` changes

```go
// Opts carries the resource limits to apply when creating a cgroup.
type Opts struct {
    MaxMemoryBytes        int64   // 0 = no limit; written to memory.max
    CPUQuota              float64 // 0 = no limit; fraction of one CPU (0.5 = 50%)
    MaxIOReadBytesPerSec  int64   // 0 = no limit; written to io.max rbps=<n>
    MaxIOWriteBytesPerSec int64   // 0 = no limit; written to io.max wbps=<n>
}
```

`Stop()` return signature is unchanged (`cpuUsec`, `memPeakBytes`, `err`).
I/O stats are read-only observability, not required for MVP — add them only if
a corresponding field is added to `ExecMetrics`.

---

## `internal/cgroups/cgroups_linux.go` — `New()` addition

After the `cpu.max` block, add:

```go
if opts.MaxIOReadBytesPerSec > 0 || opts.MaxIOWriteBytesPerSec > 0 {
    major, minor, err := devMajorMinor(sandboxDir)
    if err == nil {
        rbps := "max"
        if opts.MaxIOReadBytesPerSec > 0 {
            rbps = strconv.FormatInt(opts.MaxIOReadBytesPerSec, 10)
        }
        wbps := "max"
        if opts.MaxIOWriteBytesPerSec > 0 {
            wbps = strconv.FormatInt(opts.MaxIOWriteBytesPerSec, 10)
        }
        line := fmt.Sprintf("%d:%d rbps=%s wbps=%s", major, minor, rbps, wbps)
        if err := writeFile(filepath.Join(dir, "io.max"), line); err != nil {
            // Non-fatal: io controller may not be enabled. Log and continue.
            _ = err
        }
    }
}
```

### Helper: `devMajorMinor`

```go
import "syscall"

// devMajorMinor returns the major and minor device numbers for the filesystem
// containing path. Used to construct io.max entries.
func devMajorMinor(path string) (major, minor uint32, err error) {
    var st syscall.Stat_t
    if err = syscall.Stat(path, &st); err != nil {
        return 0, 0, err
    }
    // st.Dev is a uint64 encoding <major, minor>.
    // On Linux amd64/arm64 the encoding is: major = (dev>>8)&0xfff | (dev>>32)&~0xfff
    //                                        minor = (dev&0xff) | ((dev>>12)&~0xff)
    // Use unix.Major/Minor helpers from golang.org/x/sys/unix.
    return unix.Major(st.Dev), unix.Minor(st.Dev), nil
}
```

`golang.org/x/sys/unix` is already a transitive dep (used in the Landlock and
seccomp packages). No new dependency required.

### Problem: `New()` doesn't receive the sandbox directory

Currently `cgroups.Opts` is assembled in `exechandler.go` and does not know
the sandbox root directory. Two approaches:

**Option A (preferred)** — Pass sandbox dir through `Opts`:
```go
type Opts struct {
    // ...existing fields...
    SandboxDir string // used only to detect the block device for io.max
}
```
`exechandler.go` already has access to `hc.Dir` (the process working directory),
which is inside the sandbox root and lives on the same device.

**Option B** — Detect the device from inside `New()` using the cgroup dir itself.
Less clean because cgroups live under `/sys/fs/cgroup` which may be on a tmpfs,
not the actual sandbox device.

Use **Option A**.

---

## `isolation/exechandler.go` changes

Add to `ExecLimits`:
```go
MaxIOReadBytesPerSec  int64 // 0 = no limit
MaxIOWriteBytesPerSec int64 // 0 = no limit
```

In the cgroup creation block, pass the new fields and the working directory:
```go
if created, cgErr := limits.CgroupManager.New(id, cgroups.Opts{
    MaxMemoryBytes:        limits.MaxMemoryBytes,
    CPUQuota:              limits.CPUQuota,
    MaxIOReadBytesPerSec:  limits.MaxIOReadBytesPerSec,
    MaxIOWriteBytesPerSec: limits.MaxIOWriteBytesPerSec,
    SandboxDir:            hc.Dir,
}); cgErr == nil {
```

---

## `sandbox/options.go` changes

Add to `ResourceLimits`:
```go
// MaxIOReadMBPerSec limits disk read throughput via cgroupv2 io.max (Linux only).
// Zero means no limit.
MaxIOReadMBPerSec int

// MaxIOWriteMBPerSec limits disk write throughput via cgroupv2 io.max (Linux only).
// Zero means no limit.
MaxIOWriteMBPerSec int
```

---

## `sandbox/sandbox.go` — `wireHandlers()` changes

In the `ExecLimits` struct literal:
```go
MaxIOReadBytesPerSec:  int64(s.opts.Limits.MaxIOReadMBPerSec) * 1024 * 1024,
MaxIOWriteBytesPerSec: int64(s.opts.Limits.MaxIOWriteMBPerSec) * 1024 * 1024,
```

---

## CLI: `main.go` flag additions

```go
cmd.Flags().IntVar(&f.ioReadMB, "io-read",  0, "disk read cap in MiB/s (Linux cgroupv2 only; 0=unlimited)")
cmd.Flags().IntVar(&f.ioWriteMB, "io-write", 0, "disk write cap in MiB/s (Linux cgroupv2 only; 0=unlimited)")
```

Wire into `toOptions()`:
```go
MaxIOReadMBPerSec:  f.ioReadMB,
MaxIOWriteMBPerSec: f.ioWriteMB,
```

---

## Observability: io.stat harvesting (optional)

`io.stat` per-device bytes read/written can be harvested in `Stop()` if
I/O metrics are desired. Format:
```
<major>:<minor> rbytes=<n> wbytes=<n> rios=<n> wios=<n> dbytes=0 dios=0
```

If added, extend `ExecMetrics` in `exechandler.go`:
```go
type ExecMetrics struct {
    // ...existing...
    IOReadBytes  uint64
    IOWriteBytes uint64
}
```

And extend `ExecutionResult` in `sandbox/result.go`:
```go
IOReadBytes  int64
IOWriteBytes int64
```

This is a nice-to-have; the I/O limit itself does not require harvesting.

---

## io controller availability check

Not all cgroupv2 setups enable the `io` controller. Check before writing:

```go
func ioControllerAvailable() bool {
    data, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
    if err != nil {
        return false
    }
    return strings.Contains(string(data), "io")
}
```

Call this inside `linuxManager.New()` before attempting `io.max` write.
Cache the result in `linuxManager` for efficiency.

---

## Tests

```go
//go:build linux

func TestIOCapLimitsWrite(t *testing.T) {
    if !cgroupIOAvailable() { t.Skip("io controller not available") }
    sb := newSandbox(t, sandbox.Options{
        Limits: sandbox.ResourceLimits{
            MaxIOWriteMBPerSec: 1, // 1 MiB/s cap
            Timeout:            5 * time.Second,
        },
    })
    // Writing 10 MiB at 1 MiB/s cap should take >= 2s (kernel throttles io.max).
    start := time.Now()
    mustRun(t, sb, `dd if=/dev/zero of=/tmp/io-test bs=1M count=10 oflag=direct`)
    if time.Since(start) < 2*time.Second {
        t.Error("io cap not throttling writes as expected")
    }
}
```

---

## Key design decisions

1. **Non-fatal when io controller is absent**: Many cloud VMs and container runtimes
   disable the `io` controller. We silently skip writing `io.max` rather than
   failing sandbox creation.

2. **Device detection from working directory**: The sandbox temp directory is
   always on a real filesystem, so `stat(hc.Dir)` gives the correct device.
   The cgroup hierarchy itself (`/sys/fs/cgroup`) is a virtual fs and can't
   be used for device detection.

3. **Direct I/O (`oflag=direct`) for test**: Buffered writes might not hit the
   disk during the test window. Direct I/O bypasses page cache and ensures the
   kernel-level throttling fires.
