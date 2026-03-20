# Implementation Order

Execute in this sequence to keep the build green at every step.

## Step 1 — Create executor/intercept/ package (no existing code touched)

Create files in order (each builds on the previous):
1. `executor/intercept/intercept.go` — Config, Interceptor interface, NewDispatcher, NewAuditMiddleware
2. `executor/intercept/sysinfo.go` — pwd, whoami, hostname, id, uname
3. `executor/intercept/filesystem.go` — ls, stat, cat, head, tail, wc
4. `executor/intercept/environment.go` — env, printenv, which
5. `executor/intercept/callhook.go` — NewCallHandler (CallHandlerFunc)
6. `executor/intercept/pathhandlers.go` — NewStatHandler, NewReadDirHandler

Run: `go build ./executor/intercept/` — must compile clean.

## Step 2 — Add middleware constructors to isolation and packages packages

In `isolation/` — add `NewIsolatedExecMiddleware` alongside existing `NewIsolatedExecHandler`.
In `packages/` — add `NewShimMiddleware` alongside existing `NewShimHandler`.

Run: `go build ./isolation/... ./packages/...`

## Step 3 — Update executor/shell.go

- Replace `execHandler ExecHandlerFunc` field with `execMiddlewares []func(next ExecHandlerFunc) ExecHandlerFunc`
- Add `callHandler`, `statHandler`, `readDirHandler` fields
- Add setter methods (keep `WithExecHandler` as compat shim)
- Update `runCore()` opts assembly

Run: `go build ./executor/...` — must compile clean.
Run: `go test ./executor/...` — shell_test.go must pass.

## Step 4 — Update sandbox/options.go

Add `AuditWriter io.Writer` and `BlockList []string` to Options struct.

Run: `go build ./sandbox/...`

## Step 5 — Rewrite sandbox/sandbox.go wireHandlers()

Replace the manual closure chain with the new middleware assembly.
Add import for `executor/intercept`.
Remove import of direct `executor.ProcessInfoConfig` / `executor.NewProcessInfoHandler`.

Run: `go build ./sandbox/...`
Run: `go test ./sandbox/...` — all phase tests must pass.

## Step 6 — Delete executor/processinfo.go

```bash
git rm executor/processinfo.go
```

Verify: `grep -r "ProcessInfoConfig\|NewProcessInfoHandler" .` → zero results.

Run: `go test ./...` — full suite green.

## Verification checklist

```bash
go test ./...                                          # all green
go vet ./...                                           # no issues
grep -r "NewProcessInfoHandler" .                      # → empty
grep -r "ExecHandlers\|CallHandler\|StatHandler\|ReadDirHandler2" executor/shell.go  # → present
```
