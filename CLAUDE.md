# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Project Is

**agentic-bash** is a pure Go embedded sandbox library providing a stateful, isolated bash execution environment for AI agents. It is imported as a library (not run as a daemon) and provides persistent shell sessions with OS-level isolation — without requiring Docker, root, or external processes.

## Commands

### Build & Run (CLI)
```bash
go run main.go run script.sh
go run main.go run --cmd "pip install requests"
go run main.go shell    # interactive shell
go run main.go tui      # terminal UI
```

### Tests
```bash
go test ./...                          # all tests
go test ./sandbox -v                   # sandbox package, verbose
go test ./sandbox -run TestPhase2      # specific phase
go test ./executor -v
go test ./isolation -v
```

### Docker-based Testing Environment
```bash
make build          # build Docker image with binary compiled inside
make up-root        # start root-privileged container
make up-sudoer      # start container with passwordless sudo
make up-locked      # start container with all caps dropped
make shell-root     # exec shell into root container
make down           # stop all containers
```

## Architecture

### Layers (top to bottom)

```
Consumer (AI Agent / CLI)
    ↓
sandbox/        ← Public API: New(), Run(), RunStream(), Snapshot(), Restore()
    ↓
executor/       ← Shell execution; ShellExecutor uses mvdan.cc/sh (pure Go), no /bin/bash
    ↓
executor/intercept/  ← Intercepts calls: filesystem containment, sysinfo spoofing, path rewriting
    ↓
fs/             ← LayeredFS: read-only base + writable overlay; ChangeTracker; Snapshot/Restore
isolation/      ← IsolationStrategy interface; Landlock (Linux 5.13+), namespaces, no-op
network/        ← Network policy enforcement: Allow, Deny, Allowlist
packages/       ← pip/apt-get shims redirected into overlay, no host modification
internal/cgroups/   ← cgroupv2 resource limits (Linux only; no-op on macOS)
```

### Key Design Decisions

- **Pure Go shell interpreter** (`mvdan.cc/sh`): State (env vars, CWD, functions, history) persists across `Run()` calls within a session. This is the core innovation — no subprocess, no state loss.
- **Virtual path layer**: `/home/user`, `/tmp`, `/workspace` are synthetic; `$PWD` always shows virtual paths, never host tmpdir.
- **Layered filesystem**: Base layer (read-only) + overlay (writable). Snapshots serialize the overlay only.
- **No-op on macOS**: Shell-level illusions work everywhere; OS-level isolation (Landlock, namespaces, cgroups) is Linux-only.
- **Isolation levels**: `IsolationNone` (shell illusions only), `IsolationLandlock` (Linux LSM), `IsolationNamespace` (CLONE_NEW*), `IsolationAuto` (best available, default).

### Public API Surface (`sandbox/`)

```go
sandbox.New(opts ...Option) (*Sandbox, error)
s.Run(ctx, cmd string) ExecutionResult
s.RunStream(ctx, cmd string, stdout, stderr io.Writer) ExecutionResult
s.Reset() error
s.Snapshot() (*Snapshot, error)
s.Restore(snap *Snapshot) error
```

### Test Organization

Tests are organized by implementation phase (each phase = a feature area):

| Phase | File | Focus |
|-------|------|-------|
| 2 | `sandbox/phase2_test.go` | Function persistence, state extraction, `set -e` semantics |
| 3 | `sandbox/phase3_test.go` | Bootstrap filesystem, virtual paths, Linux environment feel |
| 5 | `sandbox/phase5_test.go` | Resource limits: timeout, memory, CPU, output cap (cgroupv2) |
| 6 | `sandbox/phase6_test.go` | Package manager shims (pip, apt-get interception) |
| 7 | `sandbox/phase7_test.go` | Snapshot/restore |

## Code Philosophy

From `contributing/go/abstractions.md`:
- Prefer functions over types; don't create types just to group functions
- Don't duplicate external structures (wrap only when adding semantics)
- Never silently discard input — callers must explicitly opt out
- Discover interfaces from need; don't predict future ones
- Wrappers must add semantics, not just rename

## Platform Notes

- Full functionality requires Linux (cgroupv2, Landlock, namespaces)
- macOS: shell-level isolation only — all OS-level isolation is no-op
- Go version: 1.25.0 (`go.mod`)
- Key external dependency: `mvdan.cc/sh/v3` for the pure-Go shell interpreter
