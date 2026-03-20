# sandbox/sandbox.go — wireHandlers() Rewrite

## Current (to be replaced)

```go
func (s *Sandbox) wireHandlers() {
    shellExec, ok := s.exec.(*executor.ShellExecutor)
    if !ok { return }

    // output cap
    if s.opts.Limits.MaxOutputMB > 0 {
        shellExec.WithOutputLimit(int64(s.opts.Limits.MaxOutputMB) * 1024 * 1024)
    }

    // network filter
    var netFilter network.Filter
    switch s.opts.Network.Mode { ... }

    limits := isolation.ExecLimits{ ... }
    isoHandler := isolation.NewIsolatedExecHandler(s.isolation, limits, s.metrics)
    shimCfg := packages.ShimConfig{ ... }
    shimHandler := packages.NewShimHandler(shimCfg, isoHandler)

    piCfg := executor.ProcessInfoConfig{ ... }
    shellExec.WithExecHandler(executor.NewProcessInfoHandler(piCfg, shimHandler))
}
```

## New

```go
func (s *Sandbox) wireHandlers() {
    shellExec, ok := s.exec.(*executor.ShellExecutor)
    if !ok { return }

    // Output cap (unchanged)
    if s.opts.Limits.MaxOutputMB > 0 {
        shellExec.WithOutputLimit(int64(s.opts.Limits.MaxOutputMB) * 1024 * 1024)
    }

    // Network filter (unchanged)
    var netFilter network.Filter
    switch s.opts.Network.Mode {
    case NetworkDeny:
        netFilter = network.NewDeny()
    case NetworkAllowlist:
        netFilter = network.NewAllowlist(s.opts.Network.Allowlist)
    default:
        netFilter = network.NewAllow()
    }

    limits := isolation.ExecLimits{
        MaxOutputBytes: int64(s.opts.Limits.MaxOutputMB) * 1024 * 1024,
        CgroupManager:  s.cgroupMgr,
        MaxMemoryBytes: int64(s.opts.Limits.MaxMemoryMB) * 1024 * 1024,
        CPUQuota:       s.opts.Limits.MaxCPUPercent / 100.0,
        NetworkFilter:  netFilter,
    }

    // ── Build intercept config ────────────────────────────────────────────────
    interceptCfg := intercept.Config{
        UserName:    s.opts.Bootstrap.UserName,
        Hostname:    s.opts.Bootstrap.Hostname,
        UID:         s.opts.Bootstrap.UID,
        GID:         s.opts.Bootstrap.GID,
        SandboxRoot: s.tempDir,
    }

    // ── ExecHandlers middleware chain (outermost → innermost) ─────────────────
    shellExec.WithExecMiddlewares(
        // 1. Audit: log every external command spawn
        intercept.NewAuditMiddleware(s.opts.AuditWriter),

        // 2. Virtual command dispatcher: sysinfo + filesystem + env shims
        intercept.NewDispatcher(
            append(
                append(
                    intercept.NewSysInfoInterceptors(interceptCfg),
                    intercept.NewFilesystemInterceptors(interceptCfg)...,
                ),
                intercept.NewEnvInterceptors(interceptCfg)...,
            )...,
        ),

        // 3. Package manager shims (pip, apt, npm)
        packages.NewShimMiddleware(packages.ShimConfig{
            OverlayRoot: s.tempDir,
            Manifest:    s.manifest,
            CacheDir:    packages.DefaultCacheDir(),
        }),

        // 4. Isolation + resource limits (innermost before DefaultExecHandler)
        isolation.NewIsolatedExecMiddleware(s.isolation, limits, s.metrics),
    )

    // ── CallHandler: audit + block list + path arg rewriting ─────────────────
    shellExec.WithCallHandler(intercept.NewCallHandler(intercept.CallConfig{
        AuditWriter: s.opts.AuditWriter,
        BlockList:   s.opts.BlockList,
        SandboxRoot: s.tempDir,
    }))

    // ── StatHandler: virtual→real for [[ -f path ]] etc. ─────────────────────
    shellExec.WithStatHandler(intercept.NewStatHandler(s.tempDir))

    // ── ReadDirHandler2: virtual→real for glob expansion ─────────────────────
    shellExec.WithReadDirHandler(intercept.NewReadDirHandler(s.tempDir))
}
```

## sandbox/options.go additions

```go
type Options struct {
    // ... existing fields ...

    // AuditWriter receives a log line for every command execution when non-nil.
    // Each line is formatted as: [HH:MM:SS.mmm] call: <args...>
    AuditWriter io.Writer

    // BlockList is a list of command patterns to deny unconditionally.
    // Each entry is prefix-matched against the full command+args string.
    // Example: []string{"rm -rf /", "mkfs", "dd if=/dev/"}
    BlockList []string
}
```

## packages and isolation middleware constructors

These are NEW companion constructors needed alongside existing ones:

### isolation package — add to existing file

```go
// NewIsolatedExecMiddleware wraps NewIsolatedExecHandler as an ExecHandlers middleware.
func NewIsolatedExecMiddleware(iso IsolationStrategy, limits ExecLimits, metrics *ExecMetrics) func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc {
    return func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc {
        return NewIsolatedExecHandler(iso, limits, metrics)
        // Note: NewIsolatedExecHandler already ignores "next" and calls real exec.
        // The DefaultExecHandler appended by interp.ExecHandlers serves as the fallback.
    }
}
```

### packages package — add to existing file

```go
// NewShimMiddleware wraps NewShimHandler as an ExecHandlers middleware.
func NewShimMiddleware(cfg ShimConfig) func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc {
    return func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc {
        return NewShimHandler(cfg, next)
    }
}
```
