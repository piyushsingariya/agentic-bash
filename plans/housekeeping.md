# Plan: Housekeeping & Code Quality

## Goal

A collection of small, targeted fixes that improve code quality, correctness,
and developer experience without adding new features.

---

## 1. `go mod tidy` — fix indirect dependency markers

**Problem**: `go.mod` marks `github.com/spf13/cobra`, `github.com/chzyer/readline`,
`github.com/spf13/pflag`, and `github.com/inconshreveable/mousetrap` as
`// indirect`. They are direct imports in `main.go` and should be listed
without the marker.

**Fix**: Run `go mod tidy` after any `go get` operations and ensure these
packages appear in the `require` block without `// indirect`.

```
go mod tidy
```

Expected result in `go.mod`:
```go
require (
    github.com/charmbracelet/bubbles v0.20.0
    github.com/charmbracelet/bubbletea v1.3.5
    github.com/charmbracelet/lipgloss v1.1.0
    github.com/chzyer/readline v1.5.1       // direct: used in main.go
    github.com/spf13/afero v1.15.0
    github.com/spf13/cobra v1.10.2           // direct: used in main.go
    golang.org/x/sys v0.42.0
    mvdan.cc/sh/v3 v3.13.0
)
```

`github.com/spf13/pflag` and `github.com/inconshreveable/mousetrap` are
transitive deps of cobra/readline and should remain `// indirect`.

---

## 2. Fix "Key Design Decisions §4" in `plan.md`

**Problem**: Section 4 of the Key Design Decisions in `plan.md` incorrectly
states that persistent session state (env/cwd/functions) is borrowed from
`vercel-labs/just-bash`. In fact, `just-bash` **resets** all state (env, cwd,
functions) after every `exec()` call. Persistent state is `agentic-bash`'s own
design, in the opposite direction.

**Fix**: Update that paragraph in `plan.md`:

```markdown
<!-- BEFORE -->
4. **Persistent session state** (borrowed from just-bash)...

<!-- AFTER -->
4. **Persistent session state** — unlike just-bash (which resets env/cwd/
   functions after every exec()), agentic-bash carries env, cwd, and function
   definitions forward across Run() calls. This makes the sandbox behave like
   a persistent shell session, which is the primary requirement for AI agents
   that issue commands sequentially.
```

---

## 3. Sandbox `Close()` should remove temp directory

**Problem**: `sandbox.New()` creates a `os.MkdirTemp` directory but `Close()`
may not clean it up (inspect `sandbox/sandbox.go` — if `os.RemoveAll` is
missing from `Close()`, there will be directory leaks in long-running processes
and tests).

**Fix** (if leak exists): Add `os.RemoveAll(s.tmpDir)` to `Close()`:

```go
func (s *Sandbox) Close() error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.closed {
        return nil
    }
    s.closed = true
    // Clean up temp directory.
    if s.tmpDir != "" {
        _ = os.RemoveAll(s.tmpDir)
    }
    return nil
}
```

Verify by searching for `os.RemoveAll` in `sandbox/sandbox.go`.

---

## 4. `TestPoolIdleEviction` sleep is too short for some CI environments

**Problem**: The test uses `time.Sleep(2 * time.Second)` to wait for idle
eviction to fire. On very slow CI machines this may flake because the eviction
loop runs at `max(ttl/2, 1s)` and the goroutine scheduler may delay it.

**Fix**: Increase the sleep to `3 * time.Second` and add a retry loop:

```go
func TestPoolIdleEviction(t *testing.T) {
    ttl := 200 * time.Millisecond
    pool := sandbox.NewPool(sandbox.PoolOptions{
        MinSize: 2, MaxSize: 4, IdleTTL: ttl,
    })
    defer pool.Close()

    initialSize := pool.Size()
    deadline := time.Now().Add(5 * time.Second)
    for time.Now().Before(deadline) {
        time.Sleep(500 * time.Millisecond)
        if pool.Size() < initialSize {
            return // eviction happened
        }
    }
    t.Errorf("idle sandboxes not evicted within 5s; size still %d", pool.Size())
}
```

---

## 5. `packages/` and `network/` are untracked

**Problem**: `git status` shows `?? network/` and `?? packages/` — these
directories exist on disk but are not committed. They may be work-in-progress
from earlier phases.

**Action**:
1. Inspect each directory to determine if it is complete and tested.
2. If complete: `git add network/ packages/` and commit.
3. If incomplete: add to `.gitignore` or add a `// TODO` notice in the directory.

Do not commit unfinished/untested packages without review.

---

## 6. `sandbox/phase6_test.go` and `sandbox/phase7_test.go` are untracked

**Problem**: `git status` shows `?? sandbox/phase6_test.go` and
`?? sandbox/phase7_test.go`. These tests are not committed.

**Action**: Run the tests, fix any failures, then commit:

```bash
go test -v ./sandbox/ -run TestPhase6
go test -v ./sandbox/ -run TestPhase7
```

If tests pass, add and commit them. If they fail, fix the underlying issues first.

---

## 7. Ensure `isolation/exechandler.go` compiles on macOS

**Problem**: `exechandler.go` uses `killProcessGroup` which is defined in
`isolation/sysprocattr_linux.go` and `isolation/sysprocattr_other.go`. Verify
that both stubs exist and that the non-Linux stub compiles cleanly:

```bash
GOOS=darwin go build ./...
```

If any Linux-only symbols leak through without build tags, add the appropriate
`//go:build linux` and `//go:build !linux` tags.

---

## 8. Add `.github/workflows/ci.yml`

**Problem**: No CI configuration exists. Without automated testing, regressions
will go undetected.

**Minimal CI workflow** (`.github/workflows/ci.yml`):

```yaml
name: CI
on: [push, pull_request]

jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
        go: ['1.25']
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go }}
      - run: go build ./...
      - run: go vet ./...
      - run: go test ./...
      - name: Integration tests (Linux only)
        if: runner.os == 'Linux'
        run: go test -tags integration ./integration/...
```

---

## 9. `go vet` and `staticcheck` clean pass

**Action**: Run and fix all warnings:

```bash
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

Common issues to watch for:
- `fmt.Println` used with format verbs (use `fmt.Printf`)
- Unused parameters in interface implementations
- Missing error checks on `defer` close calls

---

## 10. Update `README.md` with current feature set

**Problem**: The README (if it exists) likely doesn't cover Phases 6–9 features
(package shims, network policy, Pool API, RunStream, file transfer, etc.).

**Action**: Update (or create) `README.md` with:
- Quick start example
- Feature matrix (what works on Linux vs macOS)
- API overview (`sandbox.New`, `Run`, `RunStream`, `Pool`, snapshot/restore)
- Build instructions (with and without `otel` / `nobase` tags)
- CLI usage (`agentic-bash run/shell/snapshot/restore/tui`)

---

## Priority order

1. `go mod tidy` (#1) — affects `go.sum` integrity; run first.
2. Untracked files review (#5, #6) — verify and commit or ignore.
3. macOS build check (#7) — prevents CI failures.
4. Add CI (#8) — enables automated regression detection.
5. Fix plan.md (#2) — documentation correctness.
6. Test flake fix (#4) — prevents intermittent CI failures.
7. Close() leak fix (#3) — correctness; verify it's not already handled.
8. `go vet` clean pass (#9) — code quality.
9. README update (#10) — documentation.
