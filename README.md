# agentic-bash

A pure Go embedded sandbox library that gives AI agents a **stateful, isolated bash execution environment** — no Docker, no root, no external daemons.

Import it directly into any Go application. Each sandbox session maintains persistent state (environment variables, working directory, shell functions) across multiple `Run()` calls, behaving like a real interactive shell session.

---

## Why agentic-bash?

AI agents that execute bash commands need two things that are rarely solved together:

1. **Statefulness** — `cd /workspace` in one command should still be in effect for the next
2. **Isolation** — the agent must not be able to read host secrets, fill the disk, or make arbitrary network calls

Existing solutions force a tradeoff: Docker gives isolation but adds daemon overhead and breaks in-process statefulness; running `/bin/sh` subprocess per command gives real execution but leaks state. agentic-bash solves both inside a single Go library with zero external dependencies.

---

## Features

- **Stateful shell sessions** — env vars, CWD, shell functions, and command history persist across `Run()` calls
- **Pure Go shell interpreter** — uses `mvdan.cc/sh` in-process; no `/bin/bash` required
- **Linux environment feel** — synthetic `/etc`, clean identity (`whoami`, `hostname`, `id`), virtual path layer so `$PWD` shows `/home/user` not a tmpdir
- **Layered filesystem** — optional read-only base layer + writable overlay; full snapshot/restore
- **Filesystem change tracking** — every `Run()` result includes which files were created, modified, or deleted
- **Resource limits** — per-run timeout, memory cap, CPU quota, output size cap
- **Network policy** — allow-all, deny-all, or domain/CIDR allowlist
- **Package manager shims** — `pip install` and `apt-get install` intercepted and redirected into the sandbox overlay; host filesystem untouched
- **Linux isolation** — Landlock LSM (no root, Linux 5.13+) or Linux namespaces; macOS gets a no-op strategy that still provides all shell-level illusions
- **Sandbox pool** — pre-warmed reusable sandbox instances with configurable min/max size and idle TTL
- **Streaming output** — `RunStream()` pipes stdout/stderr in real-time without buffering
- **Interactive TUI** — split-panel terminal + log viewer powered by [Bubble Tea](https://github.com/charmbracelet/bubbletea)

---

## Quick Start

```go
import "github.com/piyushsingariya/agentic-bash/sandbox"

s, err := sandbox.New(sandbox.Options{
    Limits: sandbox.ResourceLimits{
        Timeout:     30 * time.Second,
        MaxMemoryMB: 512,
    },
    Network: sandbox.NetworkPolicy{
        Mode: sandbox.NetworkDeny,
    },
})
if err != nil {
    log.Fatal(err)
}
defer s.Close()

// State persists across calls
s.Run("cd /workspace && export VERSION=1.2.3")
result := s.Run("echo current dir: $PWD, version: $VERSION")

fmt.Println(result.Stdout)
// current dir: /workspace, version: 1.2.3
```

---

## Installation

```bash
go get github.com/piyushsingariya/agentic-bash
```

Requires Go 1.21+. No system dependencies on macOS. On Linux, Landlock isolation requires kernel 5.13+; falls back to no-op automatically if unavailable.

---

## Configuration

### Options

```go
type Options struct {
    // Isolation level: IsolationAuto (default), IsolationNamespace,
    // IsolationLandlock, IsolationNone
    Isolation IsolationLevel

    // Per-run resource caps
    Limits ResourceLimits

    // Egress network control
    Network NetworkPolicy

    // Synthetic Linux identity (username, hostname, UID, GID)
    Bootstrap BootstrapConfig

    // EnvPresetLinux (default): clean synthetic env, no host bleed
    // EnvPresetInheritHost: inherit macOS/Linux host environment
    // EnvPresetEmpty: only opts.Env is set
    EnvPreset EnvironmentPreset

    // Extra environment variables merged on top of the preset
    Env map[string]string

    // Initial working directory (virtual path). Default: /home/user
    WorkDir string

    // Read-only base layer directory (e.g. a pre-baked tool image)
    BaseImageDir string

    // Lifecycle hooks
    OnCommand   func(cmd string)
    OnResult    func(r ExecutionResult)
    OnViolation func(v PolicyViolation)
}
```

### Resource Limits

```go
type ResourceLimits struct {
    Timeout       time.Duration // wall-clock per Run(); 0 = no limit
    MaxMemoryMB   int           // cgroupv2 cap (Linux only); 0 = no limit
    MaxCPUPercent float64       // CPU quota as % of one core; 0 = no limit
    MaxOutputMB   int           // combined stdout+stderr cap; 0 = no limit
}
```

### Network Policy

```go
type NetworkPolicy struct {
    Mode      NetworkMode // NetworkAllow | NetworkDeny | NetworkAllowlist
    Allowlist []string    // CIDR ranges or domain names (Allowlist mode only)
    DNSServer string      // custom resolver
}
```

### Bootstrap Config

```go
type BootstrapConfig struct {
    UserName string // default "user"
    Hostname string // default "sandbox"
    UID      int    // default 1000
    GID      int    // default 1000
}
```

---

## API Reference

### Sandbox

```go
// Create a new sandbox session
func New(opts Options) (*Sandbox, error)

// Run a bash command synchronously; returns full result
func (s *Sandbox) Run(cmd string) ExecutionResult

// Run with explicit context for cancellation
func (s *Sandbox) RunContext(ctx context.Context, cmd string) ExecutionResult

// Run and stream stdout/stderr in real-time; returns exit code
func (s *Sandbox) RunStream(ctx context.Context, cmd string, stdout, stderr io.Writer) (int, error)

// Reset session state (env, CWD, functions) to initial values
func (s *Sandbox) Reset() error

// Snapshot current filesystem state to a tar archive
func (s *Sandbox) Snapshot(w io.Writer) error

// Restore filesystem state from a tar archive
func (s *Sandbox) Restore(r io.Reader) error

// Read-only view of current shell state
func (s *Sandbox) State() ShellState

// Release all resources
func (s *Sandbox) Close() error
```

### ExecutionResult

```go
type ExecutionResult struct {
    Stdout        string
    Stderr        string
    ExitCode      int
    Duration      time.Duration
    Error         error          // infrastructure error (not exit code != 0)

    // Filesystem diff for this Run()
    FilesCreated  []string
    FilesModified []string
    FilesDeleted  []string

    // Resource usage (Linux only; zero on other platforms)
    CPUTime      time.Duration
    MemoryPeakMB int
}
```

### Sandbox Pool

```go
pool, err := sandbox.NewPool(sandbox.PoolOptions{
    Min:     2,               // pre-warm 2 sandboxes
    Max:     10,              // cap at 10 concurrent sandboxes
    IdleTTL: 5 * time.Minute, // recycle sandboxes idle > 5m
    New:     func() (*sandbox.Sandbox, error) { return sandbox.New(opts) },
})

s, err := pool.Acquire(ctx)
defer pool.Release(s)
```

---

## Architecture

```
Consumer (AI agent / application)
         │
         │  sandbox.New(opts) → sandbox.Run("cmd")
         ▼
┌─────────────────────────────────────────────────────┐
│                    Sandbox                          │
│  ┌──────────────┐   ┌──────────────────────────┐   │
│  │  ShellState  │   │       Bootstrap           │   │
│  │  • env vars  │   │  /etc/hostname            │   │
│  │  • CWD       │   │  /etc/os-release          │   │
│  │  • functions │   │  /home/user/.bashrc       │   │
│  │  • history   │   │  /workspace, /tmp, ...    │   │
│  └──────┬───────┘   └──────────────────────────┘   │
└─────────┼───────────────────────────────────────────┘
          │
          ▼
┌─────────────────────┐
│   ShellExecutor     │  mvdan.cc/sh in-process interpreter
│                     │
│  ExecHandler chain: │
│  1. ProcessInfo     │  intercepts: whoami, hostname, id, uname
│  2. PackageShim     │  intercepts: pip install, apt-get install
│  3. Isolation       │  wraps real binaries with Landlock/namespace
│                     │
│  OpenHandler:       │
│  • virtual → real   │  /home/user → /tmp/agentic-xyz/home/user
│  • block escapes    │  reads outside sandbox → ENOENT
│  • allow-list       │  /dev/null, /dev/urandom, /proc/self/*
└────────┬────────────┘
         │
         ▼
┌──────────────────────────────────────────────────────┐
│                   LayeredFS                          │
│  ┌─────────────────┐    ┌──────────────────────┐    │
│  │  Base Layer     │    │  Writable Overlay     │    │
│  │  (read-only,    │    │  (tmpDir on host,     │    │
│  │   optional)     │    │   change-tracked)     │    │
│  └─────────────────┘    └──────────────────────┘    │
└──────────────────────────────────────────────────────┘
         │
         ▼
┌───────────────────────────────────────┐
│     OS Isolation (Linux only)         │
│  • Landlock LSM  (no root, 5.13+)     │
│  • CLONE_NEWNS / CLONE_NEWPID         │
│  • CLONE_NEWNET (network deny/allow)  │
│  • cgroupv2 (memory + CPU)            │
└───────────────────────────────────────┘
```

### Request lifecycle

1. `Run("pip install requests")` enters `ShellExecutor`
2. `mvdan.cc/sh` parses and starts executing
3. The `ExecHandler` chain fires — `PackageShim` intercepts `pip install`, redirects to `{tmpDir}/lib/python3/site-packages` via `pip --target`
4. For unintercepted binaries, the `IsolationHandler` wraps the child process with Landlock/namespace restrictions before `execve`
5. File operations pass through `OpenHandler`, which translates virtual paths (`/home/user/file`) to real paths (`/tmp/agentic-xyz/home/user/file`) and blocks escapes
6. After the command exits, `ChangeTracker` diffs the overlay and populates `FilesCreated/Modified/Deleted`
7. cgroups metrics are harvested (Linux), the result is returned

---

## Linux Environment Feel

By default a sandbox looks and behaves like a minimal Linux system:

```
$ pwd
/home/user

$ ls /
bin  etc  home  lib  tmp  usr  var  workspace

$ cat /etc/os-release
PRETTY_NAME="agentic-bash 1.0 (virtual)"
ID=agentic-bash

$ whoami
user

$ hostname
sandbox

$ id
uid=1000(user) gid=1000(user) groups=1000(user)

$ echo $HOSTNAME $SHELL $TERM
sandbox /bin/bash xterm-256color
```

No tmpdir paths. No macOS host variables (`HOMEBREW_PREFIX`, `TMPDIR=/var/folders/...`). No real machine identity.

This is achieved entirely in userspace — no chroot, no root privileges:

- **Virtual path layer** — `interp.Dir` points to the real tmpdir path; `$PWD` is overridden to the virtual path before each run and synced back after
- **OpenHandler** — intercepts every file open; translates virtual paths, returns `ENOENT` for reads outside the sandbox (except an explicit allow-list: `/dev/null`, `/dev/urandom`, `/proc/self/*`)
- **Process info shims** — `whoami`, `hostname`, `id`, `uname` are intercepted in the `ExecHandler` chain and return synthetic values; the real binaries are never called
- **Clean env preset** — `EnvPresetLinux` (default) builds the environment from scratch; the host environment is never inherited

---

## Package Manager Shims

Commands that install packages are intercepted before reaching the real binary:

| Command | What happens |
|---|---|
| `pip install requests` | Runs real `pip --target={overlay}/lib/python3/site-packages` |
| `pip uninstall requests` | Removes package from overlay directory |
| `apt-get install curl` | Extracts package into overlay; no host packages touched |

`PYTHONPATH` is automatically injected so packages installed via `pip` are immediately importable in the same session. The host's `site-packages` is never modified.

Packages that were installed are tracked in the session manifest and visible via `s.State().Installed`.

---

## Isolation Levels

| Level | Requires root | Platform | What it provides |
|---|---|---|---|
| `IsolationNone` | No | All | Shell-level illusions only (env, paths, process info) |
| `IsolationLandlock` | No | Linux 5.13+ | Filesystem access restricted by Landlock LSM |
| `IsolationNamespace` | No (user ns) | Linux | Mount/PID/user namespaces via `CLONE_NEW*` |
| `IsolationAuto` | No | All | Best available at runtime (default) |

On macOS, `IsolationAuto` resolves to `IsolationNone` — the full Linux-feel illusion is still in effect at the shell level, but OS-level syscall restrictions are not applied.

---

## Snapshot and Restore

Save and resume full sandbox state:

```go
// Save
f, _ := os.Create("checkpoint.tar")
s.Snapshot(f)
f.Close()

// Restore in a new session
f, _ := os.Open("checkpoint.tar")
s2, _ := sandbox.New(opts)
s2.Restore(f)

// Continue from where the previous session left off
result := s2.Run("echo still here: $MY_VAR")
```

Snapshots are tar archives of the writable overlay. Shell state (env vars, CWD, functions) is included in the archive header.

---

## CLI

agentic-bash ships a CLI for development and testing:

```bash
# Run a script file
agentic-bash run script.sh --timeout 2m --memory 1024 --isolation auto

# Run an inline command
agentic-bash run --cmd "pip install requests && python3 -c 'import requests; print(requests.__version__)'"

# Interactive REPL
agentic-bash shell
# Special REPL commands:
#   %reset               — reset sandbox state
#   %snapshot <file>     — save state to file
#   %restore <file>      — load state from file

# Save state after a command
agentic-bash snapshot --cmd "pip install pandas numpy" --out base.tar

# Restore and continue
agentic-bash restore --in base.tar

# Launch TUI (split-panel terminal + log viewer)
agentic-bash tui
agentic-bash tui --vertical   # top/bottom layout
```

---

## agentic-bash vs just-bash

[vercel-labs/just-bash](https://github.com/vercel-labs/just-bash) and agentic-bash solve the same surface problem — give an AI agent a safe bash environment — but from opposite directions.

### just-bash: Reimplementation

just-bash is a TypeScript library that reimplements the entire Unix command surface from scratch. `cat`, `grep`, `curl`, `awk`, `sed` — all ~80 of them — are TypeScript functions operating on an in-memory virtual filesystem. There are no real system binaries involved. For `python3`, it ships CPython compiled to WebAssembly via Emscripten.

**Execution model:**

```
bash.exec("cat /etc/hosts | grep localhost")
        │
        └── TypeScript parser → AST → Interpreter
                                         │
                                         ├── cat()   ← TypeScript function, reads InMemoryFs
                                         └── grep()  ← re2js regex, no OS involvement
```

**Consequences:**

- Runs in the browser and serverless (no OS at all)
- Zero host dependencies
- Packages "installed" via a mocked `pip` are never actually usable (`import requests` fails)
- Output of reimplemented commands may differ subtly from real GNU coreutils
- New commands must be hand-implemented

### agentic-bash: Redirection

agentic-bash runs **real host binaries** — it doesn't reimplement them. Instead it redirects, intercepts, and wraps execution so that side-effects land inside the sandbox rather than on the host.

**Execution model:**

```
s.Run("cat /etc/hosts | grep localhost")
        │
        └── mvdan.cc/sh parser → AST → Interpreter
                                          │
                                          ├── cat /etc/hosts
                                          │     └── OpenHandler: translates /etc/hosts
                                          │           → {tmpDir}/etc/hosts (virtual file)
                                          │
                                          └── grep localhost
                                                └── real /usr/bin/grep via execve
                                                      wrapped by Landlock/namespace
```

**Consequences:**

- Real binaries produce byte-for-byte correct output
- Packages installed via `pip install` are actually usable in the same session
- `python3`, `go`, `node`, `curl` all work if present on the host
- Requires a host OS (Linux or macOS); does not run in the browser
- Isolation depth depends on the platform (Landlock on Linux; shell-level illusions on macOS)

### Side-by-side comparison

| Capability | agentic-bash | just-bash |
|---|---|---|
| **Language** | Go | TypeScript |
| **Shell interpreter** | `mvdan.cc/sh` (Go) | Custom TypeScript parser |
| **Command execution** | Real host binaries via `execve` | TypeScript reimplementations |
| **`pip install` result** | Package is actually importable | Mocked; package not usable |
| **Filesystem** | tmpDir overlay on real FS | In-memory virtual FS |
| **State persistence** | Across `Run()` calls (env, CWD, functions) | Filesystem only; fresh shell per exec |
| **Runs in browser** | No | Yes |
| **Runs serverless** | No | Yes |
| **Root required** | No | N/A |
| **Linux isolation** | Landlock LSM / namespaces | N/A |
| **Network control** | Allow / Deny / CIDR allowlist | Disabled by default |
| **Resource limits** | Timeout, memory, CPU, output cap | Execution + token count limits |
| **Snapshot / restore** | Yes (tar archive) | No |
| **Sandbox pooling** | Yes | No |
| **TUI** | Yes (Bubble Tea) | No |
| **Primary use case** | Stateful agent sessions on a server | Serverless / browser agent tasks |

### When to choose which

**Choose agentic-bash when:**
- You need packages you install to actually work (`import requests`, `go run`, etc.)
- You're running on a server and want real binary fidelity
- You need long-lived stateful sessions (multi-turn agent conversations)
- You want OS-level isolation on Linux (Landlock, namespaces, cgroups)

**Choose just-bash when:**
- You need to run in a browser or edge serverless environment
- You only need shell scripting logic, not real package functionality
- Zero host dependencies are a hard requirement
- You're running untrusted code at massive scale (in-process WASM sandbox)

---

## Project Structure

```
agentic-bash/
├── main.go                    CLI entry point
├── sandbox/
│   ├── sandbox.go             Public API: New, Run, RunStream, Snapshot, Restore
│   ├── options.go             Options, ResourceLimits, NetworkPolicy, IsolationLevel
│   ├── session.go             ShellState (env, CWD, functions, history)
│   ├── result.go              ExecutionResult
│   ├── bootstrap.go           Virtual filesystem skeleton
│   ├── pool.go                Pre-warmed sandbox pool
│   └── *_test.go              Phase tests
├── executor/
│   ├── executor.go            Executor interface
│   ├── shell.go               ShellExecutor (mvdan.cc/sh, virtual paths, state extraction)
│   ├── native.go              NativeExecutor (/bin/sh subprocess fallback)
│   ├── processinfo.go         Process info shims (whoami, hostname, id, uname)
│   └── stateextractor.go      Extracts env/CWD/functions after each run
├── fs/
│   ├── fs.go                  SandboxFS interface
│   ├── layered.go             LayeredFS (base + overlay)
│   ├── memory.go              In-memory FS implementation
│   ├── realfs.go              OS filesystem wrapper
│   ├── tracker.go             ChangeTracker (created/modified/deleted)
│   ├── handler.go             OpenHandler (path translation, escape prevention)
│   └── snapshot.go            Snapshot / Restore (tar)
├── isolation/
│   ├── strategy.go            IsolationStrategy interface
│   ├── namespace_linux.go     CLONE_NEW* namespaces
│   ├── landlock_linux.go      Landlock LSM
│   └── *_other.go             Platform stubs (macOS, Windows)
├── network/
│   ├── filter.go              Network filter interface
│   ├── filter_linux.go        CLONE_NEWNET + allowlist enforcement
│   └── filter_other.go        Platform stub
├── packages/
│   ├── manager.go             PackageManager interface
│   ├── shim.go                NewShimHandler (ExecHandler intercept)
│   ├── pip.go                 PipShim
│   ├── apt.go                 AptShim
│   ├── manifest.go            Package manifest tracking
│   └── cache.go               Package cache layer
├── tui/
│   ├── model.go               Root Bubble Tea model
│   ├── terminal_panel.go      Input + output viewport
│   ├── log_panel.go           Log viewer
│   ├── events.go              Message types
│   └── styles.go              Lipgloss styles
├── internal/
│   ├── cgroups/               cgroupv2 CPU/memory (Linux only)
│   ├── limitwriter/           Output size cap writer
│   └── pathmap/               Virtual ↔ real path translation
└── plans/                     Phase implementation plans
```

---

## Contributing

See `contributing/go/abstractions.md` for the project's philosophy on types, interfaces, and abstractions. The short version: prefer functions over types, never silently drop data, and every abstraction must earn its place with at least two concrete implementations.

---

## License

MIT
