# executor/shell.go — Changes

## What changes

1. Replace `execHandler ExecHandlerFunc` (single handler) with `execMiddlewares []func(next ExecHandlerFunc) ExecHandlerFunc`
2. Add `callHandler`, `statHandler`, `readDirHandler` fields
3. Add corresponding setter methods
4. Wire all four in `runCore()`

## Diff (logical)

### Struct fields — replace

```go
// OLD:
execHandler ExecHandlerFunc
openHandler OpenHandlerFunc

// NEW:
execMiddlewares []func(next ExecHandlerFunc) ExecHandlerFunc
openHandler     OpenHandlerFunc
callHandler     interp.CallHandlerFunc
statHandler     interp.StatHandlerFunc
readDirHandler  interp.ReadDirHandlerFunc2
```

### Constructor — update

```go
func NewShellExecutor(env []string, cwd string) *ShellExecutor {
    return &ShellExecutor{
        parser:         syntax.NewParser(syntax.Variant(syntax.LangBash)),
        printer:        syntax.NewPrinter(),
        vars:           make(map[string]expand.Variable),
        funcs:          make(map[string]*syntax.Stmt),
        dir:            cwd,
        baseEnv:        env,
        initDir:        cwd,
        execMiddlewares: nil, // populated via WithExecMiddlewares
    }
}
```

### Setters — replace WithExecHandler, add others

```go
// Replace:
// func (e *ShellExecutor) WithExecHandler(h ExecHandlerFunc)

// With:
func (e *ShellExecutor) WithExecMiddlewares(mws ...func(next ExecHandlerFunc) ExecHandlerFunc) {
    e.execMiddlewares = mws
}

// Keep for backward compat (wraps single handler as middleware):
func (e *ShellExecutor) WithExecHandler(h ExecHandlerFunc) {
    e.execMiddlewares = []func(next ExecHandlerFunc) ExecHandlerFunc{
        func(_ ExecHandlerFunc) ExecHandlerFunc { return h },
    }
}

// New:
func (e *ShellExecutor) WithCallHandler(h interp.CallHandlerFunc) {
    e.callHandler = h
}
func (e *ShellExecutor) WithStatHandler(h interp.StatHandlerFunc) {
    e.statHandler = h
}
func (e *ShellExecutor) WithReadDirHandler(h interp.ReadDirHandlerFunc2) {
    e.readDirHandler = h
}
```

### runCore() opts assembly — replace ExecHandler with ExecHandlers

```go
// OLD:
if e.execHandler != nil {
    opts = append(opts, interp.ExecHandler(e.execHandler))
}

// NEW:
if len(e.execMiddlewares) > 0 {
    opts = append(opts, interp.ExecHandlers(e.execMiddlewares...))
}
if e.callHandler != nil {
    opts = append(opts, interp.CallHandler(e.callHandler))
}
if e.statHandler != nil {
    opts = append(opts, interp.StatHandler(e.statHandler))
}
if e.readDirHandler != nil {
    opts = append(opts, interp.ReadDirHandler2(e.readDirHandler))
}
// openHandler wiring unchanged
if e.openHandler != nil {
    opts = append(opts, interp.OpenHandler(e.openHandler))
}
```
