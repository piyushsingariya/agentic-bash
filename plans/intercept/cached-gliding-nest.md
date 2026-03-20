# Plan: Robust mvdan/sh Interceptor Layer

## Context

`NewProcessInfoHandler` (`executor/processinfo.go`) is a flat `switch` on command names that intercepts 6 commands (`pwd`, `whoami`, `hostname`, `id`, `uname`, `ls`). It's brittle, un-extensible, and misses most of mvdan/sh's hook surface. It also has a minimal `ls` that breaks on any real flag combination. As the project grows to simulate a Linux environment for AI agents, the interceptor layer needs to be registry-based, middleware-composed, and cover all four interp hook types — not just ExecHandler.

The goal: a self-contained `executor/intercept` package that replaces `processinfo.go`, uses **all four** mvdan/sh hook points, and makes adding new command shims trivial.

---

## New mvdan/sh Hook Surface (not yet used)

| Hook | API | Purpose |
|------|-----|---------|
| `CallHandler` | `CallHandlerFunc(ctx, args) ([]string, error)` | Fires for **every** command incl. builtins. Can rewrite args, block commands, log. |
| `ExecHandlers` | `func(next ExecHandlerFunc) ExecHandlerFunc` | Middleware-style chaining — cleaner than manual closure nesting. |
| `ReadDirHandler2` | `ReadDirHandlerFunc2(ctx, path) ([]fs.DirEntry, error)` | Intercepts shell glob expansion (directory reads). Needs virtual→real translation. |
| `StatHandler` | `StatHandlerFunc(ctx, name, followSymlinks) (fs.FileInfo, error)` | Intercepts `[[ -f path ]]`, `[ -d ]`, etc. Needs virtual→real translation. |

---

## New Package: `executor/intercept/`

```
executor/intercept/
  intercept.go     — Interceptor interface, Dispatcher, Config
  sysinfo.go       — pwd, whoami, hostname, id, uname
  filesystem.go    — ls (rich), stat (cmd), cat, head, tail, wc
  environment.go   — env, printenv, which
  callhook.go      — CallHandler: audit log + block list
  pathhandlers.go  — StatHandler + ReadDirHandler2 with virtual path translation
```

### Core Interface

```go
// Interceptor handles a single external command name.
type Interceptor interface {
    Name() string
    Handle(ctx context.Context, args []string) error
}

// Dispatcher is a registry-based ExecHandler middleware.
// Register interceptors; unmatched commands fall through to next.
type Dispatcher struct { ... }

func NewDispatcher(cfg Config, interceptors ...Interceptor) func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc
```

### Config (replaces ProcessInfoConfig)

```go
type Config struct {
    UserName    string
    Hostname    string
    UID, GID    int
    SandboxRoot string  // real on-disk root for virtual↔real translation
}
```

### Handler Assembly (replaces wireHandlers in sandbox.go)

Instead of manual closure nesting, use `interp.ExecHandlers(middlewares...)`:

```go
// sandbox/sandbox.go wireHandlers() — after this change:
interp.ExecHandlers(
    intercept.NewAuditMiddleware(s.auditLog),      // outermost: logs every call
    intercept.NewDispatcher(interceptCfg,           // virtual command shims
        intercept.NewSysInfoInterceptors(cfg)...,
        intercept.NewFilesystemInterceptors(cfg)...,
        intercept.NewEnvInterceptors(cfg)...,
    ),
    packages.NewShimMiddleware(shimCfg),            // pip/apt shims
    isolation.NewIsolatedExecMiddleware(iso, limits, metrics), // isolation
    // DefaultExecHandler is always last (appended by interp.ExecHandlers)
)
```

Shell executor wires three additional handlers:

```go
// executor/shell.go runCore() — new opts:
interp.CallHandler(intercept.NewCallHandler(callCfg)),   // arg rewrite + block
interp.ReadDirHandler2(intercept.NewReadDirHandler(root)), // glob virtual paths
interp.StatHandler(intercept.NewStatHandler(root)),        // [ -f ] virtual paths
```

---

## Files to Create

### `executor/intercept/intercept.go`
- `Config` struct
- `Interceptor` interface with `Name() string` + `Handle(ctx, args) error`
- `Dispatcher`: map of name → Interceptor; returns `func(next ExecHandlerFunc) ExecHandlerFunc`
- `AuditMiddleware`: wraps every exec call, writes `[cmd args...]` to an `io.Writer`

### `executor/intercept/sysinfo.go`
Replaces the switch cases in `processinfo.go`:
- `pwdInterceptor` — reads `$PWD` from HandlerContext env (unchanged)
- `whoamiInterceptor` — returns `cfg.UserName`
- `hostnameInterceptor` — returns `cfg.Hostname`; supports `-s`/`-f` flags
- `idInterceptor` — returns full `uid=N(name) gid=N(name) groups=N(name)`; supports `-u`/`-g`/`-n` flags
- `unameInterceptor` — full flag matrix (`-a`, `-s`, `-r`, `-m`, `-n`, `-o`, `-p`, `-i`)

### `executor/intercept/filesystem.go`
Replaces `handleLs` and adds new shims:
- `lsInterceptor` — supports: `-l`, `-a`, `-A`, `-h` (human sizes), `-R` (recursive), `-d` (dir-only), `-t` (sort by mtime), `-S` (sort by size), `-1` (one per line), `-F` (append type indicator); colorized output optional via `NO_COLOR`
- `statInterceptor` — `stat <path>` command; shows size, mode, mtime; translates virtual paths
- `catInterceptor` — reads files through the sandbox FS (avoids subprocess spawn for simple reads)
- `headInterceptor` / `tailInterceptor` — `-n N` flag support
- `wcInterceptor` — `-l`, `-w`, `-c` flags

### `executor/intercept/environment.go`
- `envInterceptor` — prints all env vars from `HandlerContext.Env`
- `printenvInterceptor` — prints value of named vars
- `whichInterceptor` — resolves command names against `$PATH` in sandbox (uses `interp.LookPath`)

### `executor/intercept/callhook.go`
`CallHandlerFunc` implementation:
- **Audit**: log every `CallExpr` with timestamp to per-sandbox writer
- **Block list**: configurable list of denied commands (e.g., `rm -rf /`); returns exit 1 with message
- **Path arg rewrite**: for commands that receive path arguments that are virtual, translate to real before forwarding (e.g., called as args to `find`)

### `executor/intercept/pathhandlers.go`
- `NewStatHandler(sandboxRoot string) interp.StatHandlerFunc` — translates virtual path → real, calls `os.Lstat`/`os.Stat`
- `NewReadDirHandler(sandboxRoot string) interp.ReadDirHandlerFunc2` — translates virtual path → real, calls `os.ReadDir`; used by shell globbing (`*.go`, `~/dir/*`)

---

## Files to Modify

### `executor/shell.go`
- Add fields: `callHandler interp.CallHandlerFunc`, `readDirHandler interp.ReadDirHandlerFunc2`, `statHandler interp.StatHandlerFunc`
- Add setter methods: `WithCallHandler`, `WithReadDirHandler`, `WithStatHandler`
- In `runCore()`: wire the three new handlers into `opts` when non-nil
- Change `execHandler ExecHandlerFunc` to `execMiddlewares []func(next ExecHandlerFunc) ExecHandlerFunc` to support `ExecHandlers` (middleware slice instead of single handler)
- Add `WithExecMiddlewares(middlewares ...func(next ExecHandlerFunc) ExecHandlerFunc)`

### `sandbox/sandbox.go` — `wireHandlers()`
- Replace manual closure chain with `ExecHandlers(middlewares...)`
- Create `intercept.Config` from `opts.Bootstrap`
- Pass `intercept.NewDispatcher(...)` as the sysinfo/fs middleware
- Wire `CallHandler`, `ReadDirHandler2`, `StatHandler` via new ShellExecutor setters
- Remove direct reference to `executor.NewProcessInfoHandler`

### `sandbox/options.go`
- Add `AuditWriter io.Writer` to `Options` — if set, all command executions are logged there

---

## Files to Delete

- `executor/processinfo.go` — fully replaced by `executor/intercept/sysinfo.go` + `executor/intercept/filesystem.go`

---

## Isolation/Package Shim Adaptation

`isolation.NewIsolatedExecHandler` and `packages.NewShimHandler` currently return `ExecHandlerFunc` (closure-wrapped). They need companion constructors returning the middleware signature:

```go
// isolation package — add:
func NewIsolatedExecMiddleware(iso IsolationStrategy, limits ExecLimits, metrics *ExecMetrics) func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc

// packages package — add:
func NewShimMiddleware(cfg ShimConfig) func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc
```

The old `NewIsolatedExecHandler` / `NewShimHandler` can remain as thin wrappers (or be removed in a follow-up).

---

## Verification

```bash
# All existing tests must pass
go test ./sandbox/... ./executor/...

# Confirm processinfo.go is gone and no references remain
grep -r "NewProcessInfoHandler\|ProcessInfoConfig" .

# Confirm new hooks are wired (grep for the new call sites)
grep -r "CallHandler\|ReadDirHandler2\|StatHandler" executor/shell.go

# Run specific tests that exercise the interceptor
go test ./sandbox/... -run TestPhase2 -v
go test ./sandbox/... -run TestPhase3 -v

# Manual smoke test (if CLI exists)
# ls -lah /home/user
# stat /home/user/.bashrc
# whoami; hostname; id; uname -a
# which python3
# env | grep PATH
```

---

## Critical Files

| File | Role |
|------|------|
| [executor/processinfo.go](executor/processinfo.go) | **Delete** — replaced entirely |
| [executor/shell.go](executor/shell.go) | Add new handler fields + wire 3 new interp hooks |
| [sandbox/sandbox.go](sandbox/sandbox.go) | `wireHandlers()` — switch to ExecHandlers middleware |
| [sandbox/options.go](sandbox/options.go) | Add `AuditWriter` option |
| `executor/intercept/` | **New package** — all shims live here |
| `isolation/` + `packages/` | Add middleware-signature constructors |
