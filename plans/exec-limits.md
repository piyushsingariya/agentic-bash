# Plan: In-Process Execution Limits (Loop/Recursion/Command Counts)

## Goal

Prevent runaway scripts from consuming unbounded CPU inside the **in-process
shell interpreter** (`mvdan.cc/sh`). External commands already have a wall-clock
timeout and cgroup limits; this plan covers the interpreter-internal paths:

- Infinite loops (`while true; do :; done`)
- Deep recursion (`f() { f; }; f`)
- Excessive command fan-out (scripts that spawn millions of builtins)

These scenarios can peg a CPU without ever forking an external process, making
them invisible to cgroup and timeout enforcement if the timeout is very long.

---

## Why this is needed

`mvdan.cc/sh` executes builtins, pipelines, and function calls entirely
in-process. A tight loop like:

```bash
i=0; while true; do i=$((i+1)); done
```

runs millions of iterations per second and cannot be killed by cgroups.
The wall-clock `Timeout` does fire eventually, but between iterations the
goroutine holds a CPU core completely.

The fix is to count **statements executed** inside the interpreter and cancel
the runner context when a configurable threshold is exceeded.

---

## Hook point: `interp.ExecHandler`

`interp.ExecHandler` is called for every **external command** (fork+exec
path) but NOT for builtins. There is no direct "every statement" callback.

However, `mvdan.cc/sh` exposes a way to intercept at the right level via
a custom `interp.ExecHandlerFunc` that wraps **built-in** lookup. The
interpreter's built-in table is checked first; if a command is not a
built-in the ExecHandler is called. So ExecHandler fires per external command.

For pure-builtin loops there is no per-iteration callback. The practical
solution is a **lightweight goroutine** that periodically checks whether
the script is still running and cancels when a CPU-time or wall-time budget
per "iteration window" is exhausted.

A better hook is `interp.CallHandler` (available in mvdan.cc/sh v3.10+):
```go
interp.CallHandler(func(ctx context.Context, mc interp.MacroContext, name string, args []string) ([]string, error) {
    // Called for every shell function invocation.
})
```

And `interp.ReadDirHandler2` / `interp.StatHandler` intercept file operations.
None of these fire for every statement.

**Practical approach for MVP**: Use a **statement counter** via a custom
`interp.ExecHandlerFunc` for external commands PLUS a **goroutine** that
cancels the context after `MaxStatements` external-command invocations.
For pure-builtin loops, rely on the wall-clock timeout.

For a more complete solution (covering pure-builtin loops), a separate
goroutine polls `runtime.NumGoroutine()` or uses `runtime/pprof` CPU
accounting, but this is fragile. The cleanest path is to contribute a
"statement counter" callback to upstream `mvdan.cc/sh` — but that is
out of scope here.

---

## Files to modify

| Path | Change |
|---|---|
| `sandbox/options.go` | Add `MaxCommands`, `MaxRecursionDepth` to `ResourceLimits` |
| `isolation/exechandler.go` | Add `MaxCommands`, `MaxRecursionDepth` to `ExecLimits`; implement counting wrapper |
| `sandbox/sandbox.go` | Wire new limit fields in `wireHandlers()` |
| `executor/shell.go` | Add `WithCallDepthLimit(n int)` and wire call-depth tracking via function preamble inspection |

---

## `sandbox/options.go` additions

```go
// MaxCommands limits the total number of external commands a single Run()
// may spawn. Zero means no limit.
// Example: 10000 prevents scripts that fork millions of subprocesses.
MaxCommands int

// MaxRecursionDepth limits shell function call depth. Zero means no limit.
// Prevents infinite recursion like: f() { f; }; f
// Implemented by counting function invocations via a call-depth tracker
// injected into the shell's ExecHandler chain.
MaxRecursionDepth int
```

---

## Command counter in `isolation/exechandler.go`

Add to `ExecLimits`:
```go
MaxCommands      int // 0 = unlimited
MaxRecursionDepth int // 0 = unlimited
```

Add to `ExecMetrics`:
```go
CommandCount uint64 // total external commands spawned
```

In `NewIsolatedExecHandler`, before the `cmd.Start()` block, add a counter:

```go
// Command count enforcement.
if limits.MaxCommands > 0 {
    count := atomic.AddUint64(&metrics.CommandCount, 1)
    if count > uint64(limits.MaxCommands) {
        if metrics != nil {
            metrics.mu.Lock()
            metrics.LimitHit = true
            metrics.mu.Unlock()
        }
        return fmt.Errorf("command limit (%d) exceeded", limits.MaxCommands)
    }
}
```

This returns an error from the ExecHandler, which `mvdan.cc/sh` surfaces
as a non-zero exit status from the spawning command, stopping the script.

---

## Recursion depth tracking in `executor/shell.go`

Shell recursion happens via function calls, which are **in-process** — the
ExecHandler only fires for external binaries, not for `f() { ... }; f`.

### Option A: `interp.CallHandler` (preferred, requires mvdan.cc/sh v3.10+)

```go
var depth atomic.Int64

runner, err := interp.New(
    // ...existing opts...
    interp.CallHandler(func(ctx context.Context, mc interp.MacroContext, name string, args []string) ([]string, error) {
        if maxDepth > 0 {
            d := depth.Add(1)
            defer depth.Add(-1)
            if d > int64(maxDepth) {
                return nil, fmt.Errorf("recursion depth limit (%d) exceeded: function %q", maxDepth, name)
            }
        }
        return args, nil
    }),
)
```

Check mvdan.cc/sh changelog for `CallHandler` availability. If not available,
use Option B.

### Option B: Wrapper ExecHandler counting "function-like" invocations

Since function calls inside the same shell don't go through ExecHandler,
this option is limited. Skip recursion depth for MVP; document it as a
known limitation when using `ShellExecutor`.

### Option C: Context-value depth counter via preamble injection

Inject a bash counter variable `__AGENTIC_DEPTH` into the preamble:

```bash
__AGENTIC_DEPTH=0
f() {
    __AGENTIC_DEPTH=$((__AGENTIC_DEPTH+1))
    if [ $__AGENTIC_DEPTH -gt 100 ]; then
        echo "recursion limit exceeded" >&2; return 1
    fi
    # ...original body...
    __AGENTIC_DEPTH=$((__AGENTIC_DEPTH-1))
}
```

This is invasive (requires AST rewriting of every function) and is not
safe for all scripts. **Not recommended**.

**MVP recommendation**: Implement MaxCommands via the counter in ExecHandler.
For MaxRecursionDepth, check if `interp.CallHandler` exists; if so, implement it;
otherwise document the limitation and skip for v1.

---

## `ExecMetrics` additions for sandbox result

Add to `ExecutionResult` in `sandbox/result.go`:
```go
// CommandsExecuted is the number of external commands spawned during Run().
CommandsExecuted int
```

And populate it in `sandbox/sandbox.go` after `runCore` returns:
```go
result.CommandsExecuted = int(metrics.CommandCount)
```

---

## Sandbox-level wiring (`sandbox/sandbox.go`)

In `wireHandlers()`, inside the `ExecLimits` struct literal:
```go
MaxCommands:       s.opts.Limits.MaxCommands,
MaxRecursionDepth: s.opts.Limits.MaxRecursionDepth,
```

---

## CLI flag additions (`main.go`)

```go
cmd.Flags().IntVar(&f.maxCommands, "max-commands", 0,
    "max external commands per Run() (0=unlimited)")
cmd.Flags().IntVar(&f.maxRecursion, "max-recursion", 0,
    "max shell function recursion depth (0=unlimited)")
```

Wire into `toOptions()`:
```go
MaxCommands:       f.maxCommands,
MaxRecursionDepth: f.maxRecursion,
```

---

## Tests

```go
func TestMaxCommandsLimitsForking(t *testing.T) {
    sb := newSandbox(t, sandbox.Options{
        Limits: sandbox.ResourceLimits{MaxCommands: 5},
    })
    // Each `echo` is one external command. Expect failure after 5.
    r := sb.Run(`for i in $(seq 1 10); do echo $i; done`)
    if r.ExitCode == 0 {
        t.Error("expected non-zero exit when MaxCommands is exceeded")
    }
}

func TestMaxCommandsNotTriggeredOnBuiltins(t *testing.T) {
    sb := newSandbox(t, sandbox.Options{
        Limits: sandbox.ResourceLimits{MaxCommands: 3},
    })
    // Pure builtins (echo is a builtin in mvdan.cc/sh).
    r := mustRun(t, sb, `i=0; while [ $i -lt 100 ]; do i=$((i+1)); done; echo $i`)
    if strings.TrimSpace(r.Stdout) != "100" {
        t.Errorf("builtin loop should not be limited; got %q", r.Stdout)
    }
}

func TestMaxRecursionDepth(t *testing.T) {
    if !supportsCallHandler() { t.Skip("CallHandler not available in this mvdan.cc/sh version") }
    sb := newSandbox(t, sandbox.Options{
        Limits: sandbox.ResourceLimits{MaxRecursionDepth: 10},
    })
    r := sb.Run(`recurse() { recurse; }; recurse`)
    if r.ExitCode == 0 {
        t.Error("expected non-zero exit on infinite recursion")
    }
}
```

---

## Key design decisions

1. **External commands only for MVP**: The `interp.ExecHandler` path gives a
   clean, reliable hook for external commands. Pure-builtin loops are bounded
   only by the wall-clock timeout. This is acceptable for most agent workloads
   where the dangerous patterns involve forking (compilation, data processing).

2. **Error return from ExecHandler**: Returning a non-nil error from the
   ExecHandler causes mvdan.cc/sh to propagate it as a script failure. This is
   cleaner than calling `cancel()` on the context, which would trigger the
   timeout-error path instead of a limit-exceeded message.

3. **CommandsExecuted in result**: Exposing the count allows callers to monitor
   usage without having to instrument the sandbox themselves.

4. **`interp.CallHandler` feature detection**: Since mvdan.cc/sh doesn't
   guarantee API stability for newer handler types, wrap the call in a build
   constraint or runtime check. If absent, MaxRecursionDepth is silently ignored.
