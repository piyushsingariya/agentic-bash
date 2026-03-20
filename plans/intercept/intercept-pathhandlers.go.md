# executor/intercept/pathhandlers.go

StatHandler and ReadDirHandler2 — virtual→real path translation for shell
conditionals ([[ -f /home/user/foo ]]) and glob expansion (*.go, /workspace/**).

```go
package intercept

import (
	"context"
	"io/fs"
	"os"

	"mvdan.cc/sh/v3/interp"
	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
)

// NewStatHandler returns a StatHandlerFunc that translates virtual absolute
// paths to their real on-disk counterparts before calling os.Lstat / os.Stat.
//
// This fixes [[ -f /home/user/foo.txt ]], test -d /workspace, and similar
// shell conditionals that use stat() internally — without this, they operate
// on the host path which doesn't exist.
func NewStatHandler(sandboxRoot string) interp.StatHandlerFunc {
	return func(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
		real := pathmap.VirtualToReal(sandboxRoot, name)
		if followSymlinks {
			return os.Stat(real)
		}
		return os.Lstat(real)
	}
}

// NewReadDirHandler returns a ReadDirHandlerFunc2 that translates virtual
// absolute paths to real before reading directory contents.
//
// This fixes shell glob expansion: when the shell expands /workspace/*.py,
// it calls ReadDir("/workspace"). Without translation, it tries to read the
// host /workspace which doesn't exist (or is the wrong directory).
func NewReadDirHandler(sandboxRoot string) interp.ReadDirHandlerFunc2 {
	return func(ctx context.Context, path string) ([]fs.DirEntry, error) {
		real := pathmap.VirtualToReal(sandboxRoot, path)
		return os.ReadDir(real)
	}
}
```

## Why these matter

### StatHandler
Without it, these fail silently in the sandbox:
```bash
if [[ -f /home/user/.bashrc ]]; then echo "exists"; fi
# → "exists" never printed because stat("/home/user/.bashrc") hits host path

[ -d /workspace ] && echo "ok"
# → always false
```

With it, the shell resolves `/home/user/.bashrc` → `/tmp/agentic-bash-xyz/home/user/.bashrc`
before calling stat, so the check succeeds correctly.

### ReadDirHandler2
Without it, globs fail:
```bash
ls /workspace/*.py
# → "no match" because ReadDir("/workspace") hits host, not sandbox

for f in /home/user/*.txt; do cat "$f"; done
# → loop never executes
```

With it, glob expansion reads from the real tmpdir and returns the correct entries.

## Integration in shell.go

```go
// In ShellExecutor struct — new fields:
statHandler    interp.StatHandlerFunc
readDirHandler interp.ReadDirHandlerFunc2

// New setters:
func (e *ShellExecutor) WithStatHandler(h interp.StatHandlerFunc) {
    e.statHandler = h
}
func (e *ShellExecutor) WithReadDirHandler(h interp.ReadDirHandlerFunc2) {
    e.readDirHandler = h
}

// In runCore() opts assembly:
if e.statHandler != nil {
    opts = append(opts, interp.StatHandler(e.statHandler))
}
if e.readDirHandler != nil {
    opts = append(opts, interp.ReadDirHandler2(e.readDirHandler))
}
```

## Integration in sandbox.go wireHandlers()

```go
// After creating intercept.Config:
shellExec.WithStatHandler(intercept.NewStatHandler(s.tempDir))
shellExec.WithReadDirHandler(intercept.NewReadDirHandler(s.tempDir))
```
