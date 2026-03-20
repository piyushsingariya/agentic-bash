# agentic-bash: Embedded Go Sandbox Library for AI Agents

## What We're Building

A **pure Go embeddable library** that gives AI agents a stateful, isolated execution environment — filesystem, shell, and CLI — without requiring Docker, root access, or external daemons. Think just-bash but in Go, designed to be `import`-ed directly into any AI agent.

Reference inspiration: [vercel-labs/just-bash](https://github.com/vercel-labs/just-bash)

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    CONSUMER (AI Agent)                  │
│  session := sandbox.New(opts)                           │
│  result  := session.Run("pip install requests && ...")  │
└───────────────────────────┬─────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────┐
│                      Session Layer                      │
│  Persistent state: Env vars, CWD, shell functions,      │
│  command history, installed packages                    │
└──────┬────────────────────┬────────────────────┬────────┘
       │                    │                    │
┌──────▼──────┐   ┌─────────▼────────┐  ┌───────▼──────┐
│  Executor   │   │   Filesystem     │  │  Isolation   │
│             │   │                  │  │  Strategy    │
│ • NativeExec│   │ • MemoryFS       │  │              │
│   (os/exec) │   │ • LayeredFS      │  │ • Noop       │
│             │   │   (base+overlay) │  │ • Namespace  │
│ • ShellExec │   │ • ChangeTracker  │  │ • Landlock   │
│   (mvdan/sh)│   │ • Snapshot/Rst   │  │              │
└──────┬──────┘   └─────────┬────────┘  └───────┬──────┘
       │                    │                    │
┌──────▼────────────────────▼────────────────────▼──────┐
│                  Resource Controller                   │
│    CPU limits • Memory limits • Timeout • I/O caps     │
└────────────────────────────┬───────────────────────────┘
                             │
┌────────────────────────────▼───────────────────────────┐
│              Package Manager Abstraction               │
│       Pre-baked base layers • Lazy install • Cache     │
└────────────────────────────────────────────────────────┘
```

---

## Repository Layout

```
agentic-bash/
├── go.mod
├── go.sum
├── plan.md
├── main.go                        # Example / CLI demo
│
├── sandbox/
│   ├── sandbox.go                 # Public API: New(), Run(), Reset(), Close()
│   ├── session.go                 # Session: persistent state across calls
│   ├── options.go                 # SandboxOptions, ResourceLimits, NetworkPolicy
│   ├── result.go                  # ExecutionResult: stdout, stderr, exit, fs diff
│   └── pool.go                    # SandboxPool: pre-warmed reusable sandboxes
│
├── fs/
│   ├── fs.go                      # SandboxFS interface
│   ├── memory.go                  # Pure Go in-memory FS (afero-backed)
│   ├── layered.go                 # Read-only base + writable overlay
│   ├── tracker.go                 # Tracks file creates/writes/deletes per run
│   └── snapshot.go                # Point-in-time snapshot + restore
│
├── executor/
│   ├── executor.go                # Executor interface
│   ├── native.go                  # os/exec based (real binaries on real FS)
│   └── shell.go                   # mvdan.cc/sh in-process pure Go shell
│
├── isolation/
│   ├── strategy.go                # IsolationStrategy interface
│   ├── noop.go                    # No-op (dev/macOS)
│   ├── namespace.go               # Linux namespaces (CLONE_NEWNS/PID/NET)
│   └── landlock.go                # Landlock LSM (Linux 5.13+, no root)
│
├── packages/
│   ├── manager.go                 # PackageManager interface
│   ├── base.go                    # Base layer: pre-baked tar of common tools
│   ├── apt.go                     # apt-get shimmed to overlay FS
│   ├── pip.go                     # pip shimmed to overlay FS
│   └── manifest.go                # Tracks what is installed in the sandbox
│
├── network/
│   ├── policy.go                  # NetworkPolicy: allow-all, deny-all, allowlist
│   └── namespace.go               # CLONE_NEWNET + veth setup (Linux)
│
└── internal/
    ├── cgroups/
    │   └── cgroups.go             # cgroupv2 CPU/memory enforcement (Linux)
    └── seccomp/
        └── filter.go              # syscall allowlist via go-seccomp-bpf (Linux)
```

---

## Core Types

```go
// sandbox/options.go

type IsolationLevel int
const (
    IsolationNone       IsolationLevel = iota // no-op, dev/macOS
    IsolationNamespace                        // Linux namespaces
    IsolationLandlock                         // Landlock LSM (no root)
    IsolationAuto                             // pick best available at runtime
)

type NetworkMode int
const (
    NetworkAllow     NetworkMode = iota // full host network access
    NetworkDeny                         // no external network (loopback only)
    NetworkAllowlist                    // egress to specific domains/CIDRs only
)

type NetworkPolicy struct {
    Mode      NetworkMode
    Allowlist []string // domains or CIDR ranges
    DNSServer string   // custom resolver for filtering
}

type ResourceLimits struct {
    Timeout       time.Duration // wall-clock timeout per Run()
    MaxMemoryMB   int           // memory.max in cgroupv2 (Linux only)
    MaxCPUPercent float64       // cpu.max quota (Linux only)
    MaxOutputMB   int           // stdout+stderr combined cap
    MaxFileSizeMB int           // largest single file write allowed
}

type Options struct {
    Isolation    IsolationLevel
    Limits       ResourceLimits
    Network      NetworkPolicy
    Env          map[string]string // initial environment
    WorkDir      string            // initial working directory inside sandbox
    BaseImageDir string            // path to pre-baked tool archive (optional)

    // Hooks
    OnCommand   func(cmd string)
    OnResult    func(r ExecutionResult)
    OnViolation func(v PolicyViolation)
}
```

```go
// sandbox/result.go

type ExecutionResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
    Duration time.Duration
    Error    error

    // Filesystem changes during this Run()
    FilesCreated  []string
    FilesModified []string
    FilesDeleted  []string

    // Resource usage (Linux only)
    CPUTime   time.Duration
    MemoryPeakMB int
}

type PolicyViolation struct {
    Type    string // "network", "filesystem", "syscall"
    Detail  string
    Blocked bool
}
```

```go
// sandbox/session.go

type ShellState struct {
    Env          map[string]string
    Cwd          string
    Functions    map[string]string // shell function definitions
    History      []string          // command history
    Installed    []string          // package manifest
    ExportedVars map[string]bool   // which env vars are exported
}
```

---

## Phase-by-Phase Implementation Plan

### Phase 1 — Core Foundation
**Goal: working `sandbox.Run("echo hello")` with timeout and result capture**

**Tasks:**

1. Initialize `go.mod` with module path `github.com/<org>/agentic-bash`
2. Define all core types in `sandbox/options.go` and `sandbox/result.go`
3. Implement `NativeExecutor` in `executor/native.go`:
   - Use `os/exec.CommandContext` with `context.WithTimeout`
   - Capture stdout/stderr into separate `bytes.Buffer`
   - Set `SysProcAttr.Pdeathsig = syscall.SIGKILL` to kill children when parent dies
   - Set `SysProcAttr.Setpgid = true` and kill the entire process group on timeout
4. Implement `Sandbox` struct in `sandbox/sandbox.go`:
   - `New(opts Options) *Sandbox`
   - `Run(cmd string) ExecutionResult`
   - `Close() error`
5. Implement `Session` in `sandbox/session.go`:
   - Holds `ShellState`
   - `Run()` merges session env into each command's environment
   - Updates `Cwd` after each successful `cd`-equivalent
6. Write unit tests:
   - Timeout fires and process is killed
   - Exit codes are captured correctly
   - Env vars set in one run are visible in the next

**Key dependencies:** stdlib only (`os/exec`, `context`, `bytes`, `syscall`)

---

### Phase 2 — Pure Go In-Process Shell
**Goal: execute shell scripts without requiring `/bin/bash` on the host**

**Tasks:**

1. Add `mvdan.cc/sh/v3` to `go.mod`
2. Implement `ShellExecutor` in `executor/shell.go`:
   - Parse commands via `syntax.NewParser().Parse()`
   - Execute via `interp.NewRunner()` configured with:
     - `interp.Env(expand.ListEnviron(envSlice...))` from `ShellState.Env`
     - `interp.Dir(state.Cwd)`
     - `interp.StdIO(stdin, stdout, stderr)`
3. Wire custom `interp.OpenHandler`:
   - Intercepts all file open/create calls
   - Routes them through the sandbox `SandboxFS` (Phase 3)
   - Returns `fs.ErrPermission` for paths outside sandbox root
4. Wire custom `interp.ExecHandler`:
   - Intercepts all external command invocations
   - Routes package manager commands (`apt-get`, `pip`) to shims (Phase 6)
   - Falls through to real `exec.LookPath` for everything else
5. Sync `ShellState` back after each run:
   - Capture updated env via `runner.Vars`
   - Capture updated cwd via `runner.Dir`
   - Capture defined functions via `runner.Funcs`
6. Write tests:
   - Pipes: `echo foo | tr a-z A-Z`
   - Redirections: `echo hello > /tmp/out.txt && cat /tmp/out.txt`
   - Variables: `X=1; X=$((X+1)); echo $X`
   - Loops: `for i in 1 2 3; do echo $i; done`
   - `set -e` causes abort on first error
   - Functions defined in one run are callable in the next

**Key dependency:** `mvdan.cc/sh/v3`

---

### Phase 3 — Layered Filesystem
**Goal: each sandbox has isolated, copy-on-write filesystem; host FS is untouched**

**Tasks:**

1. Define `SandboxFS` interface in `fs/fs.go`:
   ```go
   type SandboxFS interface {
       Open(name string) (fs.File, error)
       Create(name string) (fs.File, error)
       Stat(name string) (fs.FileInfo, error)
       ReadDir(name string) ([]fs.DirEntry, error)
       MkdirAll(path string, perm fs.FileMode) error
       Remove(name string) error
       Rename(oldpath, newpath string) error
       WriteFile(name string, data []byte, perm fs.FileMode) error
       ReadFile(name string) ([]byte, error)
   }
   ```
2. Implement `MemoryFS` in `fs/memory.go`:
   - Backed by `afero.MemMapFs`
   - Wraps all calls to enforce path containment within sandbox root
3. Implement `LayeredFS` in `fs/layered.go`:
   - **Lower layer**: read-only `afero.BasePathFs` pointing at pre-baked tool dir
   - **Upper layer**: writable `MemoryFS`
   - Read: check upper first, fall through to lower on miss
   - Write/Create/Remove: always go to upper layer only
   - `MkdirAll`: replicated in upper even if lower already has dir
4. Implement `ChangeTracker` in `fs/tracker.go`:
   - Wraps any `SandboxFS`
   - Records `FilesCreated`, `FilesModified`, `FilesDeleted` per `Run()` interval
   - Reset between runs; results merged into `ExecutionResult`
5. Implement `Snapshot` / `Restore` in `fs/snapshot.go`:
   - `Snapshot() ([]byte, error)`: serialize upper layer to a tar archive in memory
   - `Restore(data []byte) error`: wipe upper layer and re-populate from tar
6. Wire `ShellExecutor.OpenHandler` to route all file I/O through `LayeredFS`
7. Write tests:
   - Writes go to upper layer, lower layer unchanged
   - Reads fall through to lower when upper has no entry
   - State persists across `Run()` calls within a session
   - `Snapshot()` then `Restore()` reproduces identical filesystem state
   - File writes outside sandbox root are rejected with `ErrPermission`

**Key dependency:** `github.com/spf13/afero`

---

### Phase 4 — Isolation Backends
**Goal: pluggable OS-level isolation with graceful degradation**

**Tasks:**

1. Define `IsolationStrategy` interface in `isolation/strategy.go`:
   ```go
   type IsolationStrategy interface {
       Name() string
       Available() bool             // runtime capability probe
       Wrap(cmd *exec.Cmd) error    // mutate cmd's SysProcAttr before exec
       Apply() error                // in-process restrictions (Landlock path)
   }
   ```
2. Implement `NoopStrategy` in `isolation/noop.go`:
   - `Available()` always returns `true`
   - `Wrap()` and `Apply()` are no-ops
   - Used on macOS and in tests
3. Implement `NamespaceStrategy` in `isolation/namespace.go` (Linux only, build tag `linux`):
   - `Wrap()` sets:
     ```go
     cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWNS |
                                   syscall.CLONE_NEWPID |
                                   syscall.CLONE_NEWUSER
     cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{...}}
     cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{...}}
     ```
   - `CLONE_NEWNS`: mount namespace — sandbox cannot see or affect host mounts
   - `CLONE_NEWPID`: PID namespace — sandbox processes cannot signal host processes
   - `CLONE_NEWUSER`: user namespace — maps sandbox root to unprivileged host UID (no `sudo` needed)
   - `Available()` probes via `unix.Unshare(syscall.CLONE_NEWUSER)` in a test goroutine
4. Implement `LandlockStrategy` in `isolation/landlock.go` (Linux 5.13+, build tag `linux`):
   - `Apply()` is called inside the child process before exec (via `runtime/debug` + `syscall.ForkExec`)
   - Uses `go-landlock` to restrict allowed read/write paths to sandbox tmpdir only
   - `Available()` probes kernel version for Landlock ABI level >= 1
5. Implement auto-selector in `isolation/strategy.go`:
   - `BestAvailable() IsolationStrategy` probes in order: Landlock → Namespace → Noop
   - Called at `sandbox.New()` when `IsolationAuto` is specified
6. Write tests:
   - `NamespaceStrategy` child cannot write to host paths
   - `LandlockStrategy` rejects access to paths outside sandbox root
   - Auto-selector falls back to Noop on macOS without panicking

**Key dependency:** `github.com/shoenig/go-landlock`

---

### Phase 5 — Resource Control
**Goal: enforce CPU, memory, and I/O limits; prevent sandbox from DoS-ing the host**

**Tasks:**

1. **Process group kill on timeout** (all platforms):
   - Already seeded in Phase 1 via `SysProcAttr.Setpgid = true`
   - On context cancellation: `syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)`
   - Ensures all children in the process group are killed, not just the shell
2. **Output size cap** (all platforms):
   - Wrap `cmd.Stdout` and `cmd.Stderr` with `io.LimitedReader(w, maxBytes)`
   - When limit reached, kill the process and set `ExecutionResult.Error`
3. **cgroupv2 memory limit** (Linux, build tag `linux`) in `internal/cgroups/cgroups.go`:
   - At sandbox `New()`: create `/sys/fs/cgroup/agentic-bash/<uuid>/`
   - Write `MaxMemoryMB * 1024 * 1024` to `memory.max`
   - After `cmd.Start()`: write PID to `cgroup.procs`
   - At sandbox `Close()`: rmdir the cgroup
   - `Available()` checks for `/sys/fs/cgroup/cgroup.controllers`
4. **cgroupv2 CPU quota** (Linux):
   - Write `"<quota> <period>"` to `cpu.max` (e.g., `"50000 100000"` for 50% of one CPU)
5. **cgroupv2 I/O cap** (Linux):
   - Write `"<major>:<minor> rbps=<n> wbps=<n>"` to `io.max`
6. **Seccomp syscall allowlist** (Linux, optional hardening) in `internal/seccomp/filter.go`:
   - Use `github.com/elastic/go-seccomp-bpf`
   - Allow list: `read, write, open, openat, close, stat, fstat, lstat, mmap, mprotect, munmap, brk, rt_sigaction, rt_sigprocmask, ioctl, pread64, pwrite64, readv, writev, access, pipe, select, sched_yield, mremap, msync, mincore, madvise, shmget, shmat, shmctl, dup, dup2, pause, nanosleep, getitimer, alarm, setitimer, getpid, sendfile, socket, connect, accept, sendto, recvfrom, sendmsg, recvmsg, shutdown, bind, listen, getsockname, getpeername, socketpair, setsockopt, getsockopt, clone, fork, vfork, execve, exit, wait4, kill, uname, fcntl, flock, fsync, fdatasync, truncate, ftruncate, getdents, getcwd, chdir, fchdir, rename, mkdir, rmdir, creat, link, unlink, symlink, readlink, chmod, fchmod, chown, fchown, lchown, umask, gettimeofday, getrlimit, getrusage, sysinfo, times, ptrace [DENY], getuid, syslog [DENY], getgid, setuid [DENY], setgid [DENY], geteuid, getegid, setpgid, getppid, getpgrp, setsid, setreuid [DENY], setregid [DENY], getgroups, setgroups [DENY], setresuid [DENY], setresgid [DENY], getresuid, getresgid, getpgid, setfsuid [DENY], setfsgid [DENY], getsid, capget, capset [DENY], rt_sigpending, rt_sigtimedwait, rt_sigqueueinfo, rt_sigsuspend, sigaltstack, utime, mknod, uselib [DENY], personality [DENY], ustat [DENY], statfs, fstatfs, sysfs [DENY], getpriority, setpriority, sched_setparam, sched_getparam, sched_setscheduler, sched_getscheduler, sched_get_priority_max, sched_get_priority_min, sched_rr_get_interval, mlock, munlock, mlockall, munlockall, vhangup [DENY], modify_ldt [DENY], pivot_root [DENY], _sysctl [DENY], prctl, arch_prctl, adjtimex [DENY], setrlimit, chroot [DENY], sync, acct [DENY], settimeofday [DENY], mount [DENY], umount2 [DENY], swapon [DENY], swapoff [DENY], reboot [DENY], sethostname [DENY], setdomainname [DENY], iopl [DENY], ioperm [DENY], create_module [DENY], init_module [DENY], delete_module [DENY], get_kernel_syms [DENY], query_module [DENY], quotactl [DENY], nfsservctl [DENY], getpmsg [DENY], putpmsg [DENY], afs_syscall [DENY], tuxcall [DENY], security [DENY], gettid, readahead, setxattr [DENY], lsetxattr [DENY], fsetxattr [DENY], getxattr, lgetxattr, fgetxattr, listxattr, llistxattr, flistxattr, removexattr [DENY], lremovexattr [DENY], fremovexattr [DENY], tkill, time, futex, sched_setaffinity, sched_getaffinity, set_thread_area, io_setup [DENY], io_destroy [DENY], io_getevents [DENY], io_submit [DENY], io_cancel [DENY], get_thread_area, lookup_dcookie [DENY], epoll_create, epoll_ctl_old [DENY], epoll_wait_old [DENY], remap_file_pages [DENY], getdents64, set_tid_address, restart_syscall, semtimedop, fadvise64, timer_create, timer_settime, timer_gettime, timer_getoverrun, timer_delete, clock_settime [DENY], clock_gettime, clock_getres, clock_nanosleep, exit_group, epoll_wait, epoll_ctl, tgkill, utimes, vserver [DENY], mbind [DENY], set_mempolicy [DENY], get_mempolicy [DENY], mq_open, mq_unlink, mq_timedsend, mq_timedreceive, mq_notify, mq_getsetattr, kexec_load [DENY], waitid, add_key [DENY], request_key [DENY], keyctl [DENY], ioprio_set, ioprio_get, inotify_init, inotify_add_watch, inotify_rm_watch, migrate_pages [DENY], openat, mkdirat, mknodat, fchownat, futimesat, newfstatat, unlinkat, renameat, linkat, symlinkat, readlinkat, fchmodat, faccessat, pselect6, ppoll, unshare [DENY], set_robust_list, get_robust_list, splice, tee, sync_file_range, vmsplice, move_pages [DENY], utimensat, epoll_pwait, signalfd, timerfd_create, eventfd, fallocate, timerfd_settime, timerfd_gettime, accept4, signalfd4, eventfd2, epoll_create1, dup3, pipe2, inotify_init1, preadv, pwritev, rt_tgsigqueueinfo, perf_event_open [DENY], recvmmsg, fanotify_init [DENY], fanotify_mark [DENY], prlimit64, name_to_handle_at [DENY], open_by_handle_at [DENY], clock_adjtime [DENY], syncfs, sendmmsg, setns [DENY], getcpu, process_vm_readv [DENY], process_vm_writev [DENY], kcmp [DENY], finit_module [DENY], sched_setattr, sched_getattr, renameat2, seccomp [DENY], getrandom, memfd_create, kexec_file_load [DENY], bpf [DENY], execveat, userfaultfd [DENY], membarrier, mlock2, copy_file_range, preadv2, pwritev2, pkey_mprotect [DENY], pkey_alloc [DENY], pkey_free [DENY], statx, io_pgetevents, rseq, pidfd_send_signal [DENY], io_uring_setup [DENY], io_uring_enter [DENY], io_uring_register [DENY], open_tree [DENY], move_mount [DENY], fsopen [DENY], fsconfig [DENY], fsmount [DENY], fspick [DENY], pidfd_open, clone3, close_range, openat2, pidfd_getfd [DENY], faccessat2, process_madvise [DENY], epoll_pwait2, mount_setattr [DENY], quotactl_fd [DENY], landlock_create_ruleset [DENY], landlock_add_rule [DENY], landlock_restrict_self [DENY]`
   - Applied to child process before exec
7. Write tests:
   - Process killed when memory cgroup limit is hit
   - CPU quota causes measurable throttling
   - Output truncated and process killed when output cap exceeded
   - Seccomp blocks `mount` syscall, allows `read`/`write`

**Key dependency:** `github.com/elastic/go-seccomp-bpf`

---

### Phase 6 — Package Manager Shims
**Goal: `apt-get install`, `pip install` work transparently; changes land in sandbox overlay**

**Tasks:**

1. **Base layer pre-population** in `packages/base.go`:
   - Embed a minimal tar archive via `//go:embed base.tar.gz`
   - Archive contains: `bash`, `curl`, `wget`, `git`, `python3`, `pip3`, `gcc`, `make`, `jq`, common coreutils
   - On `New()`: extract into a tmpdir as the `LayeredFS` lower layer
   - Cache extracted base dir globally (keyed by archive checksum); reuse across sandboxes
2. Define `PackageManager` interface in `packages/manager.go`:
   ```go
   type PackageManager interface {
       Install(ctx context.Context, pkg string) error
       Uninstall(ctx context.Context, pkg string) error
       IsInstalled(pkg string) bool
       Installed() []PackageInfo
   }

   type PackageInfo struct {
       Name    string
       Version string
       Manager string // "apt", "pip", "npm", etc.
   }
   ```
3. Implement `AptShim` in `packages/apt.go`:
   - Intercepted via `ShellExecutor.ExecHandler` when command is `apt-get` or `apt`
   - Downloads `.deb` packages from Debian mirrors into a per-sandbox cache dir
   - Extracts `.deb` with `ar` + `tar` into `<overlay_root>/usr/`
   - Updates manifest
   - Honors `DEBIAN_FRONTEND=noninteractive`; suppresses interactive prompts
4. Implement `PipShim` in `packages/pip.go`:
   - Intercepted when command is `pip`, `pip3`, or `python -m pip`
   - Translates `pip install <pkg>` to:
     `pip install --target=<overlay_root>/lib/python3/site-packages <pkg>`
   - Updates manifest; records version via `importlib.metadata`
5. Implement shared download cache in `packages/base.go`:
   - Cache dir: `~/.cache/agentic-bash/packages/`
   - Keyed by `<manager>/<package>@<version>` hash
   - Shared across all sandboxes on the same host; concurrent access via file lock
6. Implement `PackageManifest` in `packages/manifest.go`:
   - Serializable `[]PackageInfo` list
   - Persisted in sandbox `ShellState`
   - Included in `Snapshot` output (Phase 3)
7. Write tests:
   - `pip install requests` → `import requests` works in next `Run()`
   - Installed package is visible in sandbox FS overlay, not in host FS
   - Reinstall is a no-op (cached); cache hit measured by timing
   - Manifest correctly reflects installed packages after snapshot/restore

---

### Phase 7 — Network Control
**Goal: control outbound network access per sandbox; deny by default for untrusted agents**

**Tasks:**

1. Define `NetworkPolicy` in `network/policy.go` (see Core Types above)
2. Implement `NetworkMode_Allow` (default):
   - No special configuration; child inherits host network stack
3. Implement `NetworkMode_Deny` (Linux) in `network/namespace.go`:
   - Add `syscall.CLONE_NEWNET` to `NamespaceStrategy.Cloneflags`
   - Child gets isolated network namespace with only `lo` (loopback) interface
   - DNS lookups fail; TCP connections to external IPs fail
   - `Available()` requires `NamespaceStrategy.Available()`
4. Implement `NetworkMode_Allowlist` (Linux):
   - Create net namespace with veth pair connecting to host bridge
   - Use `github.com/vishvananda/netlink` to:
     - Create veth pair (`veth0` in sandbox ns, `veth1` in host ns)
     - Add iptables OUTPUT rules in sandbox namespace: `ACCEPT` for allowlisted CIDRs/ports, `DROP` all else
     - Configure NAT on host side for sandbox traffic
   - DNS: route port 53 through filtering resolver that checks domain allowlist
5. **macOS fallback**: `NetworkMode_Deny` and `NetworkMode_Allowlist` log a warning and degrade to `NetworkMode_Allow` (network namespaces not available on macOS)
6. Write tests:
   - `Deny` mode: `curl https://example.com` exits non-zero
   - `Deny` mode: `curl http://localhost:8080` succeeds (loopback allowed)
   - `Allowlist` mode: request to allowlisted domain succeeds, non-listed domain fails
   - `Allow` mode: full outbound access works

**Key dependency:** `github.com/vishvananda/netlink`

---

### Phase 8 — SandboxPool & Production API
**Goal: production-ready API for AI agents — fast startup, concurrent use, observability**

**Tasks:**

1. Implement `SandboxPool` in `sandbox/pool.go`:
   ```go
   type Pool struct {
       opts    Options
       pool    chan *Sandbox
       minSize int
       maxSize int
       idleTTL time.Duration
   }

   func NewPool(opts Options, minSize, maxSize int) *Pool
   func (p *Pool) Acquire(ctx context.Context) (*Sandbox, error)
   func (p *Pool) Release(s *Sandbox)
   func (p *Pool) Close() error
   ```
   - Background goroutine pre-warms `minSize` sandboxes at startup (base layer unpacked, ready to use)
   - `Acquire()` returns a warm sandbox instantly if available; creates new one if pool empty (up to `maxSize`)
   - `Release()` calls `s.Reset()` then returns sandbox to channel; discards if pool full
   - Idle sandboxes older than `idleTTL` are drained and closed
2. Implement streaming `Run` variant:
   ```go
   func (s *Sandbox) RunStream(ctx context.Context, cmd string, stdout, stderr io.Writer) (int, error)
   ```
   - Writes stdout/stderr in real time as process produces output
   - Returns exit code when process completes
3. Implement file transfer API in `sandbox/sandbox.go`:
   ```go
   func (s *Sandbox) WriteFile(path string, data []byte) error
   func (s *Sandbox) ReadFile(path string) ([]byte, error)
   func (s *Sandbox) ListFiles(dir string) ([]FileInfo, error)
   func (s *Sandbox) UploadTar(r io.Reader) error    // batch file injection
   func (s *Sandbox) DownloadTar(w io.Writer) error  // batch file extraction
   ```
4. Implement event hooks in `sandbox/sandbox.go`:
   - `OnCommand(cmd string)`: called before each `Run()` starts
   - `OnResult(r ExecutionResult)`: called after each `Run()` completes
   - `OnViolation(v PolicyViolation)`: called when a policy rule is triggered (may or may not block)
5. Implement `Reset()` in `sandbox/sandbox.go`:
   - Wipe upper FS layer (replace with fresh `MemoryFS`)
   - Reset `ShellState` to initial values from `Options.Env` and `Options.WorkDir`
   - Keep base layer in place (no re-unpack needed)
   - Fast: O(1) allocation, not proportional to base layer size
6. OpenTelemetry integration (optional build tag `otel`):
   - Wrap `Run()` with `tracer.Start(ctx, "sandbox.Run")`
   - Add span attributes: `exit_code`, `duration_ms`, `command_hash`, `memory_peak_mb`, `cpu_time_ms`
   - Export metrics: `sandbox.run.duration`, `sandbox.run.count`, `sandbox.pool.size`, `sandbox.pool.wait_time`
7. Write tests:
   - Pool pre-warms to `minSize` before first `Acquire()`
   - Concurrent `Acquire()` from 50 goroutines all succeed within timeout
   - `Release()` + `Acquire()` reuses sandbox with clean state
   - Idle sandboxes are discarded after `idleTTL`
   - `RunStream()` delivers output incrementally (verified with chunked producer)

---

### Phase 9 — CLI Demo & Integration Tests
**Goal: standalone binary demonstrating all capabilities; doubles as integration test harness**

**Tasks:**

1. Implement CLI in `main.go` using `github.com/spf13/cobra`:
   ```
   agentic-bash shell                   # interactive REPL session
   agentic-bash run <script.sh>         # run a script file in sandbox
   agentic-bash run --cmd "echo hello"  # run inline command
   agentic-bash snapshot --out <file>   # snapshot sandbox state to file
   agentic-bash restore --in <file>     # restore from snapshot and attach shell
   ```
2. Flags for `run` subcommand:
   - `--timeout 30s`
   - `--memory 256m`
   - `--cpu 50` (percent)
   - `--network deny|allow|allowlist`
   - `--allowlist "github.com,pypi.org"`
   - `--isolation auto|namespace|landlock|none`
   - `--env KEY=VALUE` (repeatable)
   - `--workdir /workspace`
   - `--output-cap 10m`
3. Interactive REPL (`shell` subcommand):
   - Uses `github.com/chzyer/readline` for line editing + history
   - Persistent `Session` across REPL entries
   - Shows exit code and duration after each command
   - `%reset` meta-command wipes sandbox state
   - `%snapshot <file>` and `%restore <file>` meta-commands
4. Integration tests (in `integration/` directory, run with `go test -tags integration`):
   - Full pipeline: install package → use it → snapshot → restore → verify state
   - Concurrent sessions: 20 parallel sandboxes each installing different packages
   - Network deny: verify no outbound connections possible
   - Resource limits: verify OOM kill and timeout kill work end-to-end
   - macOS: verify graceful degradation (no panic when namespace isolation unavailable)

**Key dependencies:** `github.com/spf13/cobra`, `github.com/chzyer/readline`

---

## Dependency List

| Package | Purpose | Required |
|---|---|---|
| `mvdan.cc/sh/v3` | Pure Go bash interpreter (no `/bin/bash` required) | Yes |
| `github.com/spf13/afero` | Filesystem abstraction + in-memory FS | Yes |
| `github.com/spf13/cobra` | CLI framework | Yes (CLI only) |
| `github.com/chzyer/readline` | REPL line editing + history | Yes (CLI only) |
| `github.com/shoenig/go-landlock` | Unprivileged path-based isolation (Linux 5.13+) | Optional |
| `github.com/elastic/go-seccomp-bpf` | Syscall allowlist filtering (Linux) | Optional |
| `github.com/vishvananda/netlink` | Network namespace + iptables management (Linux) | Optional |
| `go.opentelemetry.io/otel` | Distributed tracing + metrics | Optional |

**Zero CGO required** for the core path. All optional Linux features compile cleanly on macOS via build tags and degrade to no-ops at runtime.

---

## Platform Support Matrix

| Feature | Linux | macOS | Notes |
|---|---|---|---|
| Shell execution | ✅ | ✅ | Pure Go shell via `mvdan.cc/sh` — no host bash required |
| In-memory filesystem isolation | ✅ | ✅ | Afero `MemMapFs` — fully cross-platform |
| Layered FS (base + overlay) | ✅ | ✅ | Pure Go implementation |
| Timeout + output caps | ✅ | ✅ | `context.WithTimeout` + `io.LimitedReader` |
| Process group kill | ✅ | ✅ | `Setpgid` + kill signal to `-pgid` |
| File transfer API | ✅ | ✅ | Operates on virtual FS layer |
| Package install shims (pip/apt) | ✅ | ✅ | Overlay FS receives install artifacts |
| Snapshot / Restore | ✅ | ✅ | tar-based serialization of upper FS layer |
| SandboxPool | ✅ | ✅ | Goroutine-based, cross-platform |
| PID namespace isolation | ✅ | ❌ | `CLONE_NEWPID` — Linux only |
| Mount namespace isolation | ✅ | ❌ | `CLONE_NEWNS` — Linux only |
| User namespace (no root) | ✅ | ❌ | `CLONE_NEWUSER` — Linux only |
| Memory limit enforcement | ✅ | ❌ | cgroupv2 `memory.max` — Linux only |
| CPU quota enforcement | ✅ | ❌ | cgroupv2 `cpu.max` — Linux only |
| I/O bandwidth cap | ✅ | ❌ | cgroupv2 `io.max` — Linux only |
| Landlock path restrictions | ✅ | ❌ | Linux 5.13+ only, no root needed |
| Seccomp syscall filter | ✅ | ❌ | Linux only |
| Network namespace (deny) | ✅ | ❌ | `CLONE_NEWNET` — Linux only |
| Network allowlist | ✅ | ❌ | netlink + iptables — Linux only |

**macOS** provides filesystem + shell isolation with process-level (not OS-level) guarantees. Sufficient for trusted agent code running on developer machines.

**Linux** provides the full hardened stack: namespaces + cgroups + landlock + seccomp + network isolation. Suitable for production multi-agent deployments.

---

## Key Design Decisions & Rationale

### 1. Pure Go shell (`mvdan.cc/sh`) over `os/exec("bash")`
Agents don't need bash installed on the host. The shell is deterministic, hookable at the Go level, and its `OpenHandler`/`ExecHandler` interfaces are what enable transparent filesystem virtualization and package manager interception. Without this, FS virtualization would require chroot (needs root) or a user-mode filesystem (FUSE) — both operationally heavy.

### 2. Layered FS with Afero overlay, not chroot or FUSE
`chroot` requires root. FUSE has high per-syscall overhead and needs kernel modules. Layered Afero in pure Go is zero-privilege, zero-overhead, and the `ShellExecutor.OpenHandler` makes it transparent to shell scripts. Trade-off: only shell-initiated file I/O goes through the virtual FS; native binaries invoked via `NativeExecutor` see the real host filesystem (mitigated by `NamespaceStrategy` on Linux).

### 3. Strategy pattern for isolation
Different callers have different threat models and operating environments. Forcing root or VM on dev machines kills adoption. Strategy pattern lets the library work out of the box on macOS (Noop), get Linux namespace isolation in CI, and get full hardening in production — same API, zero code changes.

### 4. Persistent session state across `Run()` calls
This is the defining characteristic borrowed from just-bash. AI agents issue sequences of related commands: install a tool, configure it, run it, inspect output. Each `Run()` must see env vars and cwd changes from the previous one. Most container-based approaches lose this state (new subprocess per call). The `ShellState` struct solves this explicitly.

### 5. SandboxPool with pre-warming
AI agents often run many parallel, short-lived tasks. Base layer extraction (decompressing the tool archive) takes 100-500ms. The pool does this work once at startup and amortizes it across all agent invocations. `Release()` + `Reset()` is O(1) (fresh MemMapFs allocation), not O(base layer size).

### 6. No daemon, no Docker, no root
The entire library is a Go `import`. No sidecar process, no Unix socket, no container runtime, no privilege escalation. Operators just compile and run. This is the key differentiation from E2B, Modal, and similar managed services.

---

## What's Intentionally Out of Scope

| Feature | Reason |
|---|---|
| **Firecracker / microVM support** | Needed only for untrusted third-party code; adds 125ms+ cold start and requires KVM (`CAP_SYS_ADMIN`). Out of scope for an embedded library; a separate `firecracker` backend could be added as a plugin later. |
| **Windows support** | Linux namespace APIs don't exist on Windows. Would require Hyper-V isolation strategy — a significant separate effort. |
| **Full OCI / Docker compatibility** | We are not building a container runtime. We're building a sandboxed execution environment that is lighter and embeddable. |
| **Remote execution / agent-as-a-service** | The library runs in-process. Adding a network RPC layer (gRPC/HTTP) is a thin wrapper that callers can build; it's not part of the core library. |
| **GUI / browser-based terminal** | Out of scope; a WebSocket bridge to `RunStream()` is trivial for callers to implement. |

---

## Implementation Order & Dependencies Between Phases

```
Phase 1 (Core + NativeExecutor)
    └── Phase 2 (ShellExecutor)        ← needs Phase 1 types
        └── Phase 3 (Layered FS)       ← wired into ShellExecutor hooks
            ├── Phase 4 (Isolation)    ← wraps NativeExecutor cmd
            ├── Phase 5 (Resources)    ← wraps both executors
            └── Phase 6 (Packages)     ← wired into ShellExecutor ExecHandler + Phase 3 FS
                └── Phase 7 (Network)  ← integrated into Phase 4 isolation strategy
                    └── Phase 8 (Pool + API)  ← orchestrates all above
                        └── Phase 9 (CLI + Integration Tests)
```

Phases 4, 5, 6, and 7 can be developed in parallel once Phase 3 is complete.

---

## Testing Strategy

| Test Type | Location | What's Covered |
|---|---|---|
| Unit tests | `*_test.go` alongside each package | Individual functions, edge cases, error paths |
| Integration tests | `integration/` with `-tags integration` | Full sandbox lifecycle, package install, network policy |
| Platform tests | CI matrix: `ubuntu-latest`, `macos-latest` | Graceful degradation on macOS; full isolation on Linux |
| Fuzz tests | `fs/`, `executor/` | Malformed commands, path traversal attempts, unicode |
| Benchmark tests | `sandbox/`, `pool/` | `Run()` latency, pool acquire time, concurrent throughput |

---

## Success Criteria

- `sandbox.New(opts).Run("echo hello")` works on macOS and Linux with zero configuration
- A Python script that does `pip install requests; python3 -c "import requests; print(requests.get('https://httpbin.org/get').status_code)"` executes end-to-end inside the sandbox
- Sandbox filesystem writes are not visible on the host filesystem
- A `Run()` that exceeds `ResourceLimits.Timeout` is killed within 100ms of the deadline
- On Linux, a process that exceeds `MaxMemoryMB` is killed by the OOM handler before affecting the host
- `SandboxPool.Acquire()` returns a warm sandbox in < 5ms (after pool is pre-warmed)
- The library compiles and all unit tests pass on macOS without any build errors or panics
- The library has no CGO dependencies in the core path

---

## Gap Analysis vs vercel-labs/just-bash

Evaluated against [vercel-labs/just-bash](https://github.com/vercel-labs/just-bash) (TypeScript virtual bash environment for AI agents) on 2026-03-20.

---

### 1. Features in just-bash not planned in agentic-bash

| Feature | just-bash | agentic-bash | Notes |
|---|---|---|---|
| **Custom commands API** | `defineCommand()`, `customCommands`, `registerCommand()` — inject arbitrary built-ins | No plugin/extension API | Useful for AI agents that need tool-specific commands |
| **Restrict available built-ins** | `commands` option to allowlist which built-ins are callable | No equivalent | Security hardening for untrusted scripts |
| **AST transform plugin API** | `registerTransformPlugin()`, `transform()` — expose parsed AST | No AST exposure | Useful for analysis, fuzzing, and instrumentation |
| **Browser / WASM support** | Runs in-browser via `browser.ts` entrypoint | Linux/macOS native only | Not a stated goal; out of scope for Go |
| **AI SDK tool integration** | `bash-tool` wrapper for direct AI SDK use | No first-class AI tool adapter | Low-effort wrapper to add in Phase 9 |
| **Portable sandbox API** | Vercel Sandbox-compatible interface for swapping backends | No portability layer | Allows upgrading to VM isolation transparently |
| **Virtual process info** | `processInfo` option spoofs `$$`, `$UID`, `$HOSTNAME` | Real host PIDs/UIDs visible | Information disclosure risk for untrusted scripts |
| **Per-exec `stdin`** | `stdin` per `exec()` call | Not exposed per-call | Minor gap; easy to add to `RunOptions` |
| **Binary data handling** | Handles images/compressed files via latin1 encoding | Binary stdout behavior undocumented | Relevant for `cat` on binary files |
| **Threat model document** | Comprehensive `THREAT_MODEL.md` with 65+ attack vectors and mitigations | None | Should be written after Phase 9 |

---

### 2. Planned in agentic-bash but not yet implemented

| Phase | Feature | File | Status |
|---|---|---|---|
| Phase 8 | `SandboxPool` | `sandbox/pool.go` | Missing |
| Phase 8 | `RunStream()` streaming output | `sandbox/sandbox.go` | Missing |
| Phase 8 | `UploadTar` / `DownloadTar` batch file API | `sandbox/sandbox.go` | Missing |
| Phase 8 | `Reset()` | `sandbox/sandbox.go` | Missing |
| Phase 8 | OpenTelemetry integration | `sandbox/sandbox.go` | Missing |
| Phase 9 | Cobra CLI (`run`, `shell`, `snapshot`, `restore`) | `main.go` | Missing (`main.go` is TUI-only) |
| Phase 9 | Integration test suite | `integration/` | Missing |
| Phase 5 | Seccomp BPF syscall filter | `internal/seccomp/filter.go` | Missing |

---

### 3. Fine-grained in-process execution limits

just-bash enforces configurable limits **inside the interpreter**, per `exec()` call:

- Max recursion / call depth
- Max total command count per execution
- Max loop iterations
- Max heredoc size

agentic-bash relies on wall-clock timeout + cgroupv2 memory/CPU (Linux-only). There are no per-call command/loop iteration counts enforced at the interpreter level. This means a tight infinite loop on macOS (where cgroups are unavailable) will spin indefinitely until the timeout fires.

**Recommendation**: Add an `ExecutionLimits` sub-struct to `ResourceLimits` and wire it into the `mvdan.cc/sh` runner via a custom `interp.ExecHandler` counter.

---

### 4. Architectural note on session persistence

The plan states that persistent shell state across `Run()` calls is "borrowed from just-bash." This is incorrect — just-bash actually **resets** env vars, cwd, and functions on each `exec()` call (only the filesystem persists across calls). agentic-bash's persistent session model is its **own design decision** and is a genuine differentiator. The rationale in the Key Design Decisions section should be updated to reflect this accurately.
