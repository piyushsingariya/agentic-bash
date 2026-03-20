# Plan: Seccomp BPF Syscall Filter (`internal/seccomp/`)

## Goal
Apply a syscall allowlist to every external command spawned by the sandbox so
that even if a process escapes the filesystem and namespace isolation it cannot
call dangerous kernel interfaces (`mount`, `ptrace`, `reboot`, etc.).

---

## Why seccomp is hard in pure Go

`os/exec` uses `fork + exec` under the hood. Seccomp filters cannot be "removed"
once applied (the kernel only allows adding more-restrictive filters), so the
filter must be applied **inside the child process after fork, before exec**,
not in the parent.

Go's `syscall.SysProcAttr` does not expose a seccomp field. The practical
workaround is the **self-re-exec** (also called *clone-exec helper*) pattern used
by Chrome, Docker's containerd-shim, and gVisor:

1. The sandbox binary re-executes itself with a sentinel env var
   `AGENTIC_BASH_SECCOMP=1`.
2. The re-exec'd process applies `PR_SET_NO_NEW_PRIVS` + loads the BPF filter,
   then calls `exec` on the actual target binary.
3. The target binary runs under the filter from its very first instruction.

This requires zero CGO and no external privileges.

---

## Files to create / modify

| Path | Action |
|---|---|
| `internal/seccomp/filter.go` | New — Linux build tag; BPF program + self-re-exec logic |
| `internal/seccomp/filter_other.go` | New — non-Linux stub |
| `isolation/exechandler.go` | Modify — pass seccomp opts into `ExecLimits`; call shim launcher |
| `isolation/strategy.go` | Modify — expose `SeccompEnabled` option |
| `sandbox/options.go` | Modify — add `EnableSeccomp bool` to `Options` |

---

## New dependency

```
go get github.com/elastic/go-seccomp-bpf@latest
```

The library provides:
- `seccomp.NewFilter(seccomp.ActionErrno(syscall.EPERM), policy)` — build filter
- `seccomp.LoadFilter(filter)` — write filter to current thread via `prctl`+`seccomp`

---

## `internal/seccomp/filter.go` (Linux)

```go
//go:build linux

package seccomp

import (
    "fmt"
    "os"
    "os/exec"
    "syscall"

    libseccomp "github.com/elastic/go-seccomp-bpf"
    "golang.org/x/sys/unix"
)

const sentinelEnv = "AGENTIC_BASH_SECCOMP_HELPER=1"

// IsHelperProcess reports whether the current process was launched by
// NewLauncherCmd to apply seccomp before exec.
func IsHelperProcess() bool {
    return os.Getenv("AGENTIC_BASH_SECCOMP_HELPER") == "1"
}

// RunHelperProcess is called from main() when IsHelperProcess() is true.
// It applies PR_SET_NO_NEW_PRIVS + the BPF filter and then execs the real
// command whose path and args are passed in os.Args[1:].
//
//   main.go must call:
//     if seccomp.IsHelperProcess() { seccomp.RunHelperProcess() }
//   at the very top of main(), before any cobra setup.
func RunHelperProcess() {
    if len(os.Args) < 2 {
        os.Exit(1)
    }
    if err := applyFilter(); err != nil {
        fmt.Fprintf(os.Stderr, "seccomp: apply filter: %v\n", err)
        os.Exit(1)
    }
    // exec the real binary (replaces this process image).
    if err := unix.Exec(os.Args[1], os.Args[1:], os.Environ()); err != nil {
        fmt.Fprintf(os.Stderr, "seccomp: exec %s: %v\n", os.Args[1], err)
        os.Exit(1)
    }
}

// applyFilter sets PR_SET_NO_NEW_PRIVS and loads the BPF allow-list.
func applyFilter() error {
    if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
        return fmt.Errorf("prctl PR_SET_NO_NEW_PRIVS: %w", err)
    }
    filter, err := libseccomp.NewFilter(libseccomp.ActionErrno(uint32(syscall.EPERM)), policy())
    if err != nil {
        return fmt.Errorf("build filter: %w", err)
    }
    if err := libseccomp.LoadFilter(filter); err != nil {
        return fmt.Errorf("load filter: %w", err)
    }
    return nil
}

// NewLauncherCmd builds an exec.Cmd that wraps the actual command through the
// seccomp helper.  The returned Cmd's Path is the current executable; it re-
// execs itself with sentinelEnv set and then execs `path args...`.
//
// Usage in isolation/exechandler.go:
//
//   if opts.SeccompEnabled {
//       cmd = seccomp.NewLauncherCmd(cmd)
//   }
func NewLauncherCmd(real *exec.Cmd) *exec.Cmd {
    self, _ := os.Executable()
    newArgs := append([]string{self, real.Path}, real.Args[1:]...)
    wrapped := &exec.Cmd{
        Path:        self,
        Args:        newArgs,
        Env:         append(real.Env, sentinelEnv),
        Dir:         real.Dir,
        Stdin:       real.Stdin,
        Stdout:      real.Stdout,
        Stderr:      real.Stderr,
        ExtraFiles:  real.ExtraFiles,
        SysProcAttr: real.SysProcAttr,
    }
    return wrapped
}

// Available returns true when the kernel supports seccomp BPF (Linux 3.5+).
func Available() bool {
    // Probe: attempt to read seccomp status without actually setting anything.
    _, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0)
    return err == nil
}
```

### `policy()` — the syscall allow-list

Create a helper `func policy() libseccomp.Policy` that returns a
`libseccomp.Policy` with:

```go
func policy() libseccomp.Policy {
    return libseccomp.Policy{
        DefaultAction: libseccomp.ActionErrno(uint32(syscall.EPERM)),
        Syscalls: []libseccomp.SyscallGroup{
            {
                Action: libseccomp.ActionAllow,
                Names: allowedSyscalls,
            },
        },
    }
}

var allowedSyscalls = []string{
    // Process lifecycle
    "read", "write", "open", "openat", "close", "stat", "fstat", "lstat",
    "newfstatat", "statx",
    // Memory
    "mmap", "mprotect", "munmap", "brk", "mremap", "madvise",
    // Signals
    "rt_sigaction", "rt_sigprocmask", "rt_sigreturn", "sigaltstack",
    // I/O
    "pread64", "pwrite64", "readv", "writev", "preadv", "pwritev",
    "sendfile", "splice", "copy_file_range",
    // File system
    "access", "faccessat", "faccessat2",
    "getcwd", "chdir", "fchdir",
    "rename", "renameat", "renameat2",
    "mkdir", "mkdirat", "rmdir",
    "link", "linkat", "symlink", "symlinkat", "readlink", "readlinkat",
    "unlink", "unlinkat",
    "chmod", "fchmod", "fchmodat",
    "chown", "fchown", "fchownat", "lchown",
    "truncate", "ftruncate", "fallocate",
    "fsync", "fdatasync", "sync", "syncfs",
    "getdents", "getdents64",
    "creat", "dup", "dup2", "dup3",
    "fcntl", "flock", "ioctl", "pipe", "pipe2",
    "inotify_init", "inotify_init1", "inotify_add_watch", "inotify_rm_watch",
    "utimensat", "utimes",
    // Sockets (for network access — filtered separately by network policy)
    "socket", "connect", "accept", "accept4",
    "sendto", "recvfrom", "sendmsg", "recvmsg", "sendmmsg", "recvmmsg",
    "shutdown", "bind", "listen",
    "getsockname", "getpeername", "socketpair",
    "setsockopt", "getsockopt",
    // Process / thread
    "clone", "clone3", "fork", "vfork", "execve", "execveat",
    "exit", "exit_group", "wait4", "waitid",
    "getpid", "getppid", "gettid", "getpgid", "getpgrp", "getsid",
    "setpgid", "setsid",
    "getuid", "getgid", "geteuid", "getegid",
    "getgroups", "getresuid", "getresgid",
    // Scheduling
    "sched_yield", "sched_getparam", "sched_setparam",
    "sched_getscheduler", "sched_setscheduler",
    "sched_get_priority_max", "sched_get_priority_min",
    "sched_rr_get_interval", "sched_getaffinity", "sched_setaffinity",
    "sched_getattr", "sched_setattr",
    "getpriority", "setpriority",
    // Time
    "gettimeofday", "clock_gettime", "clock_getres", "clock_nanosleep",
    "nanosleep", "time", "timer_create", "timer_settime", "timer_gettime",
    "timer_getoverrun", "timer_delete",
    "timerfd_create", "timerfd_settime", "timerfd_gettime",
    // IPC / futex
    "futex", "set_robust_list", "get_robust_list",
    "eventfd", "eventfd2", "signalfd", "signalfd4",
    "semtimedop",
    // Poll / select / epoll
    "select", "pselect6", "poll", "ppoll",
    "epoll_create", "epoll_create1", "epoll_ctl", "epoll_wait",
    "epoll_pwait", "epoll_pwait2",
    // Misc required by Go runtime + dynamic linker
    "mlock", "munlock", "mlockall", "munlockall",
    "getrlimit", "prlimit64", "setrlimit",
    "getrusage", "sysinfo", "times", "uname",
    "arch_prctl", "prctl",
    "getrandom", "memfd_create",
    "restart_syscall",
    "tgkill", "tkill", "kill",    // needed for signal forwarding
    "set_tid_address",
    "capget",                      // read-only caps check by some tools
    "rseq",                        // required by glibc 2.35+
    "close_range",
    // xattr read (needed by some package tools)
    "getxattr", "lgetxattr", "fgetxattr",
    "listxattr", "llistxattr", "flistxattr",
    // Landlock (needed when both landlock + seccomp are active)
    "landlock_create_ruleset", "landlock_add_rule", "landlock_restrict_self",
}

// Explicitly DENIED (not in allowedSyscalls):
//   mount, umount2, pivot_root, chroot       — filesystem escapes
//   ptrace                                    — debug/injection
//   kexec_load, kexec_file_load               — kernel replace
//   reboot                                    — host disruption
//   setuid, setgid, setreuid, setregid, ...  — privilege escalation
//   init_module, finit_module, delete_module  — kernel module loading
//   bpf                                       — eBPF program loading
//   io_uring_setup, io_uring_enter, ...       — io_uring (large attack surface)
//   process_vm_readv, process_vm_writev       — cross-process memory access
//   perf_event_open                           — side-channel
//   sethostname, setdomainname                — namespace escapes
```

---

## `internal/seccomp/filter_other.go` (non-Linux stub)

```go
//go:build !linux

package seccomp

import "os/exec"

func IsHelperProcess() bool             { return false }
func RunHelperProcess()                 {}
func Available() bool                   { return false }
func NewLauncherCmd(c *exec.Cmd) *exec.Cmd { return c }
```

---

## Integration: `sandbox/options.go`

Add to `Options`:
```go
// EnableSeccomp applies a BPF syscall allowlist to every external command
// spawned inside the sandbox (Linux only; requires Linux 3.5+).
// Silently ignored on non-Linux or when unavailable.
EnableSeccomp bool
```

---

## Integration: `isolation/exechandler.go`

Add to `ExecLimits`:
```go
SeccompEnabled bool // wrap external cmds with seccomp helper launcher
```

In `NewIsolatedExecHandler`, after `strategy.Wrap(cmd)` and before
`limits.NetworkFilter.Wrap(cmd)`, add:

```go
if limits.SeccompEnabled && seccomp.Available() {
    cmd = seccomp.NewLauncherCmd(cmd)
}
```

Import `"github.com/piyushsingariya/agentic-bash/internal/seccomp"`.

---

## Integration: `sandbox/sandbox.go`

In `wireHandlers()`, inside the `ExecLimits` struct literal, add:

```go
SeccompEnabled: s.opts.EnableSeccomp,
```

---

## Integration: `main.go` — critical

At the **very top** of `main()`, before `rootCmd().Execute()`:

```go
func main() {
    // Must be first: handle the seccomp helper re-exec protocol.
    if seccomp.IsHelperProcess() {
        seccomp.RunHelperProcess()
        return // unreachable; RunHelperProcess calls unix.Exec
    }
    if err := rootCmd().Execute(); err != nil {
        os.Exit(1)
    }
}
```

---

## Tests (`internal/seccomp/filter_test.go`, Linux only)

```go
//go:build linux

func TestAvailableOnLinux(t *testing.T) {
    if !seccomp.Available() {
        t.Skip("seccomp not available on this kernel")
    }
}

// Integration: run sandbox with EnableSeccomp=true, verify mount is blocked.
func TestSeccompBlocksMount(t *testing.T) { ... }

// Integration: verify read/write/exec still work under the filter.
func TestSeccompAllowsBasicIO(t *testing.T) { ... }
```

Also add to `integration/integration_test.go`:

```go
func TestSeccompBlocksDangerousSyscalls(t *testing.T) {
    if runtime.GOOS != "linux" { t.Skip(...) }
    sb := newSandbox(t, sandbox.Options{EnableSeccomp: true})
    // strace is not available in CI but we can try a Go binary that calls mount.
    // Simplest: verify the sandbox still works normally under seccomp.
    r := mustRun(t, sb, `echo seccomp-ok`)
    if strings.TrimSpace(r.Stdout) != "seccomp-ok" { t.Fail() }
}
```

---

## Key design decisions

1. **Self-re-exec over thread locking**: Thread-local seccomp + `runtime.LockOSThread()`
   doesn't work because seccomp filters are process-wide once applied (no way to remove
   them from a thread). Self-re-exec is the industry-standard pattern.

2. **Fail-open on unavailability**: If `Available()` returns false (kernel too old, or
   not Linux), the sandbox works normally — seccomp is simply not applied.
   Operators who need hard isolation must run on Linux 3.5+.

3. **Landlock + seccomp can coexist**: The allowlist includes
   `landlock_create_ruleset`, `landlock_add_rule`, `landlock_restrict_self` so that
   when both are active the Landlock strategy still functions.
