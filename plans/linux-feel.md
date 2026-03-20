# Plan: Linux Environment Feel

## Problem

A new sandbox does not feel like a Linux environment. The specific failures are:

1. **CWD leaks tmpdir** — `$PWD` shows `/tmp/agentic-bash-abc123`, not `/home/user`
2. **No directory skeleton** — `ls /` or `cd /etc` fails because the virtual FS root is
   empty; no `/home`, `/tmp`, `/usr`, `/etc` exist
3. **Host env bleeds in** — when `opts.Env == nil` the entire macOS host environment
   is inherited: `TMPDIR=/var/folders/...`, `HOMEBREW_PREFIX=...`, etc.
4. **No synthetic identity** — `echo $USER`, `echo $HOSTNAME`, `whoami`, `id` all show
   real host values or fail
5. **Reads fall through to host** — `cat /etc/hostname` returns the real machine name
   because reads outside `tmpDir` are not blocked by the current `OpenHandler`

---

## Goal

When an AI agent opens a sandbox it should see:

```
$ pwd
/home/user
$ ls /
bin  etc  home  lib  tmp  usr  var  workspace
$ echo $USER $HOSTNAME $SHELL
agent sandbox /bin/bash
$ cat /etc/os-release
PRETTY_NAME="agentic-bash 1.0 (virtual)"
ID=agentic-bash
$ id
uid=1000(agent) gid=1000(agent) groups=1000(agent)
```

No tmpdir paths. No host leakage. Stateful across `Run()` calls. Works on macOS (no
root, no namespace) and Linux (full namespace mode).

---

## Architecture

```
 New(opts)
    │
    ├─ 1. allocate tmpDir  (physical FS root)
    │
    ├─ 2. bootstrap()      (create virtual skeleton inside tmpDir)
    │       /etc/hostname, /etc/os-release, /etc/passwd, /etc/shells
    │       /home/user/, /tmp/, /workspace/, /usr/local/bin/, /var/log/
    │
    ├─ 3. buildEnv()       (construct clean Linux env from preset + overrides)
    │       HOME=/home/user  USER=agent  HOSTNAME=sandbox
    │       SHELL=/bin/bash  TERM=xterm-256color
    │       PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:…
    │
    ├─ 4. ShellState.Cwd = virtualHome   ("/home/user" or opts.WorkDir)
    │
    └─ 5. ShellExecutor sees:
            interp.Dir  → realPath(virtualCwd)  (tmpDir + "/home/user")
            env[PWD]    → virtualCwd            ("/home/user")
            OpenHandler → translatePath()       (virtual → real, block escapes)
```

---

## Phase A — Directory Skeleton

**File:** `sandbox/bootstrap.go` (new)

Create a `bootstrapFS(root string, cfg BootstrapConfig)` function called from `New()`.

### Directories to create

```
{root}/bin
{root}/etc
{root}/home/{cfg.UserName}
{root}/lib
{root}/lib64
{root}/tmp                    (mode 1777, sticky bit)
{root}/usr
{root}/usr/bin
{root}/usr/lib
{root}/usr/local
{root}/usr/local/bin
{root}/usr/local/lib
{root}/usr/sbin
{root}/var
{root}/var/log
{root}/var/tmp
{root}/workspace              (default WorkDir if none specified)
```

### Files to create

| Virtual path | Content |
|---|---|
| `/etc/hostname` | `{cfg.Hostname}\n` |
| `/etc/os-release` | See below |
| `/etc/passwd` | `{cfg.UserName}:x:{cfg.UID}:{cfg.GID}:{cfg.UserName}:/home/{cfg.UserName}:/bin/bash\nroot:x:0:0:root:/root:/bin/bash\n` |
| `/etc/group` | `{cfg.UserName}:x:{cfg.GID}:\nroot:x:0:\n` |
| `/etc/shells` | `/bin/sh\n/bin/bash\n` |
| `/etc/resolv.conf` | `nameserver 8.8.8.8\nnameserver 8.8.4.4\n` |
| `/home/{cfg.UserName}/.bashrc` | Minimal prompt + aliases (see below) |
| `/home/{cfg.UserName}/.profile` | `[ -f ~/.bashrc ] && . ~/.bashrc\n` |

`/etc/os-release`:
```
PRETTY_NAME="agentic-bash 1.0 (virtual)"
NAME="agentic-bash"
ID=agentic-bash
VERSION_ID="1.0"
HOME_URL="https://github.com/piyushsingariya/agentic-bash"
```

`/home/user/.bashrc`:
```bash
export PS1='\u@\h:\w\$ '
alias ll='ls -la'
alias l='ls -CF'
```

### BootstrapConfig (fields in Options)

```go
// sandbox/options.go additions

type BootstrapConfig struct {
    UserName string // default "user"
    Hostname string // default "sandbox"
    UID      int    // default 1000
    GID      int    // default 1000
}

// In Options:
Bootstrap BootstrapConfig
```

---

## Phase B — Clean Environment Preset

**File:** `sandbox/session.go` — replace `newShellState()`

### EnvironmentPreset enum

```go
// sandbox/options.go

type EnvironmentPreset int

const (
    // EnvPresetLinux: clean synthetic Linux env (default).
    // PATH, HOME, USER, HOSTNAME etc. are set to virtual values.
    // Host environment is NOT inherited.
    EnvPresetLinux EnvironmentPreset = iota

    // EnvPresetInheritHost: current behaviour — inherit entire host environment.
    // Useful for dev/macOS where you want real tools on PATH.
    EnvPresetInheritHost

    // EnvPresetEmpty: only opts.Env is set, nothing else.
    EnvPresetEmpty
)

// In Options:
EnvPreset EnvironmentPreset
```

### Linux preset base env

```go
func linuxBaseEnv(cfg BootstrapConfig, workDir string) map[string]string {
    return map[string]string{
        "HOME":    "/home/" + cfg.UserName,
        "USER":    cfg.UserName,
        "LOGNAME": cfg.UserName,
        "HOSTNAME": cfg.Hostname,
        "SHELL":   "/bin/bash",
        "TERM":    "xterm-256color",
        "LANG":    "en_US.UTF-8",
        "LC_ALL":  "en_US.UTF-8",
        "PATH":    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "PWD":     workDir,
        "OLDPWD":  workDir,
        "TMPDIR":  "/tmp",
    }
}
```

Caller-provided `opts.Env` values are merged on top (override).
The overlay PATH injection (existing `injectOverlayEnv`) runs after this.

---

## Phase C — Virtual Path Layer

This is the core of the "CWD leaks tmpdir" problem.

### The problem in detail

`mvdan.cc/sh`'s `interp.Dir` must be a **real OS path** that exists on disk.
The shell's `$PWD` reflects whatever was passed to `interp.Dir` — so if
`interp.Dir = /tmp/agentic-bash-abc123/home/user`, then `echo $PWD` prints the
tmpdir path.

### Solution: PWD injection + path translation

**In `executor/shell.go`:**

```go
// Before constructing runner options, translate virtual CWD to real CWD:
realDir := virtualToReal(e.sandboxRoot, e.dir)   // e.g. /tmp/.../home/user

opts := []interp.RunnerOption{
    interp.Env(expand.ListEnviron(e.effectiveEnv()...)),
    interp.Dir(realDir),
    interp.StdIO(nil, outW, errW),
}
```

`effectiveEnv()` must force `PWD` to the **virtual** path:

```go
func (e *ShellExecutor) effectiveEnv() []string {
    env := envMapToSlice(e.baseEnv)
    // Always override PWD with virtual path so $PWD looks right
    env = append(env, "PWD="+e.dir)          // e.dir is the virtual path
    env = append(env, "OLDPWD="+e.oldDir)    // track OLDPWD too
    return env
}
```

### Path translation helpers

**File:** `sandbox/pathmap.go` (new)

```go
// virtualToReal converts a virtual absolute path (e.g. /home/user/foo)
// to its real on-disk counterpart ({sandboxRoot}/home/user/foo).
// Paths that are already under sandboxRoot are returned as-is.
func virtualToReal(root, virtual string) string

// realToVirtual is the inverse.
func realToVirtual(root, real string) string

// isEscaping returns true if real resolves outside root (traversal attempt).
func isEscaping(root, real string) bool
```

### OpenHandler update

The current `OpenHandler` in `fs/handler.go` already routes writes through the
virtual FS. Extend it to:

1. Translate incoming paths: if path starts with sandboxRoot, it's already real;
   otherwise treat it as virtual and prepend sandboxRoot.
2. For reads outside sandboxRoot that are NOT explicitly allowed, return
   `fs.ErrNotExist` instead of falling through to the host. This prevents
   `cat /etc/hostname` returning the real hostname.

**Allow-list for host passthrough reads** (these are safe/necessary):

```
/dev/null
/dev/urandom
/dev/random
/proc/self/fd/*   (needed by some Go stdlib operations)
```

Everything else either hits the virtual FS or gets `ENOENT`.

### CWD sync after Run()

After each `Run()`, sync the runner's final directory back to `ShellState.Cwd`
using `realToVirtual`:

```go
// In ShellExecutor.Run() after runner.Run():
if runner.Dir != "" {
    e.dir = realToVirtual(e.sandboxRoot, runner.Dir)
}
```

---

## Phase D — Process Info Spoofing

**File:** `executor/shell.go` — extend `ExecHandler`

Intercept the following commands and return virtual output:

| Command | Spoofed output |
|---|---|
| `hostname` | `{cfg.Hostname}` |
| `whoami` | `{cfg.UserName}` |
| `id` | `uid={cfg.UID}({cfg.UserName}) gid={cfg.GID}({cfg.UserName}) groups={cfg.GID}({cfg.UserName})` |
| `uname -n` | `{cfg.Hostname}` |
| `uname -a` | `Linux {cfg.Hostname} 6.1.0-agentic #1 SMP x86_64 GNU/Linux` |

These are intercepted in the existing `ExecHandler` chain alongside the package
manager shims. Add a `processInfoHandler` that fires before the package shim handler.

Inject special variables into ShellState that `mvdan.cc/sh` will expand:

```go
// After bootstrap, inject into initial env:
env["HOSTNAME"] = cfg.Hostname    // $HOSTNAME
// $$ (PID) and $UID are read-only shell specials; handled by the id/whoami shims
```

---

## Phase E — Linux Namespace Mount (Linux-only improvement)

On Linux with `NamespaceStrategy`, add `CLONE_NEWNS` + a bind mount so the
sandbox root truly becomes `/` inside the child process:

```go
// In isolation/namespace.go, when wrapping a cmd:
cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNS

// In the child (via /proc/self/fd trick or SysProcAttr.Chroot):
// bind-mount sandboxRoot → / inside the namespace
```

This makes `NativeExecutor` commands (real binaries) also see the virtual paths.
Without this, `os.Getwd()` inside a native binary returns the real tmpdir path.

**macOS**: skip silently (Noop strategy already in place). The virtual-path
injection from Phase C handles the shell-level illusion on macOS.

---

## Phase F — Options & Defaults

Update `sandbox.New()` defaults so zero-config gives the Linux feel:

```go
// sandbox/sandbox.go — in New(), after opts normalization:

if opts.Bootstrap.UserName == "" {
    opts.Bootstrap.UserName = "user"
}
if opts.Bootstrap.Hostname == "" {
    opts.Bootstrap.Hostname = "sandbox"
}
if opts.Bootstrap.UID == 0 {
    opts.Bootstrap.UID = 1000
}
if opts.Bootstrap.GID == 0 {
    opts.Bootstrap.GID = 1000
}

// Default WorkDir to virtual home, not tmpdir
if opts.WorkDir == "" {
    opts.WorkDir = "/home/" + opts.Bootstrap.UserName
}

// Default preset to Linux feel
// (existing behaviour preserved via EnvPresetInheritHost)
```

`EnvPresetLinux` becomes the default. Users who want the old behavior pass
`EnvPreset: sandbox.EnvPresetInheritHost`.

---

## Phase G — Tests

| Test | What it verifies |
|---|---|
| `TestPWDIsVirtual` | `echo $PWD` returns `/home/user`, not tmpdir |
| `TestCdPersists` | `cd /workspace && pwd` then next `Run("pwd")` returns `/workspace` |
| `TestLsRoot` | `ls /` lists `bin etc home tmp usr var workspace` |
| `TestEtcHostname` | `cat /etc/hostname` returns `sandbox`, not real hostname |
| `TestWhoami` | `whoami` returns `agent` |
| `TestHostname` | `hostname` returns `sandbox` |
| `TestId` | `id` returns `uid=1000(user) gid=1000(user)` |
| `TestEnvNoHostLeak` | `env` output contains no macOS-specific vars (HOMEBREW_*, TMPDIR=/var/folders/...) |
| `TestCatDevNull` | `echo x > /dev/null` succeeds |
| `TestNoHostEtcLeak` | `cat /etc/hosts` returns virtual content, not host content |
| `TestWorkspaceDir` | `/workspace` exists and is writable |
| `TestBashrc` | `source ~/.bashrc && echo $PS1` does not error |

---

## Implementation Order

```
Phase A (bootstrap skeleton)       — no deps, start here
    └── Phase B (env preset)       — depends on BootstrapConfig
        └── Phase C (path layer)   — depends on A+B for real/virtual paths
            ├── Phase D (process spoofing)   — depends on ExecHandler (already exists)
            ├── Phase E (namespace mount)    — Linux only, independent of C
            └── Phase F (defaults)           — wire A+B+C into New()
                └── Phase G (tests)          — covers everything
```

Phases D and E can be done in parallel after Phase C.

---

## Files Touched

| File | Change |
|---|---|
| `sandbox/bootstrap.go` | **new** — `bootstrapFS()`, `BootstrapConfig` |
| `sandbox/pathmap.go` | **new** — `virtualToReal`, `realToVirtual`, `isEscaping` |
| `sandbox/options.go` | Add `BootstrapConfig`, `EnvironmentPreset` to `Options` |
| `sandbox/sandbox.go` | Call `bootstrapFS()`, apply new defaults in `New()` |
| `sandbox/session.go` | Replace `newShellState()` with preset-aware version |
| `executor/shell.go` | Virtual CWD in `interp.Dir`, force `PWD` in env, sync back after run |
| `fs/handler.go` | Block host reads outside sandbox; translate virtual paths |
| `isolation/namespace.go` | Add bind-mount for `CLONE_NEWNS` (Linux only) |
| `sandbox/bootstrap_test.go` | **new** — Phase G tests |

---

## Non-Goals

- Full `/proc` virtualization (too complex; allow host passthrough for `/proc/self/*`)
- Virtual network interfaces (`/sys/class/net`) — out of scope
- setuid/setgid simulation — not needed; shims cover `id`/`whoami`
- Windows support — not planned
