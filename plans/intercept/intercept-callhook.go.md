# executor/intercept/callhook.go

CallHandlerFunc — fires for EVERY command including builtins.
Used for: audit logging, block list enforcement, virtual path arg rewriting.

```go
package intercept

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"mvdan.cc/sh/v3/interp"
	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
)

// CallConfig configures the CallHandler behaviour.
type CallConfig struct {
	// AuditWriter receives a log line for every command invocation.
	// Nil disables audit logging.
	AuditWriter io.Writer

	// BlockList is a list of exact command+arg patterns to deny.
	// Each entry is matched against the joined args string.
	// Example: ["rm -rf /", "mkfs"]
	// Matching is prefix-based on the full args slice joined by spaces.
	BlockList []string

	// SandboxRoot enables virtual→real path rewriting in args.
	// When set, any argument that looks like a virtual absolute path
	// (starts with / but is NOT under SandboxRoot) is translated.
	SandboxRoot string
}

// NewCallHandler returns a CallHandlerFunc that:
//  1. Logs every invocation to cfg.AuditWriter (if set).
//  2. Denies commands matching cfg.BlockList.
//  3. Rewrites virtual path arguments to real paths for external commands.
//
// The returned args are then executed normally by the Runner.
// Returning an error halts the runner entirely, so block list violations
// write to stderr and return a non-zero exit via a sentinel approach.
//
// NOTE: CallHandler cannot return an exit code directly — it either returns
// rewritten args (nil error) or halts the runner (non-nil error).
// To "fail" a blocked command without halting, inject a replacement arg
// that causes a controlled failure, or write to stderr and return a
// deliberate exit-status error.
func NewCallHandler(cfg CallConfig) interp.CallHandlerFunc {
	return func(ctx context.Context, args []string) ([]string, error) {
		if len(args) == 0 {
			return args, nil
		}

		hc := interp.HandlerCtx(ctx)

		// ── 1. Audit logging ─────────────────────────────────────────────────
		if cfg.AuditWriter != nil {
			fmt.Fprintf(cfg.AuditWriter, "[%s] call: %s\n",
				time.Now().UTC().Format("15:04:05.000"),
				strings.Join(args, " "),
			)
		}

		// ── 2. Block list ────────────────────────────────────────────────────
		joined := strings.Join(args, " ")
		for _, pattern := range cfg.BlockList {
			if strings.HasPrefix(joined, pattern) {
				fmt.Fprintf(hc.Stderr,
					"agentic-bash: command blocked by policy: %s\n", joined)
				// Return an exit status error to abort just this command.
				return nil, interp.NewExitStatus(1)
			}
		}

		// ── 3. Virtual path rewriting ────────────────────────────────────────
		// Only rewrite args for external commands (not builtins like cd, echo).
		// Heuristic: if args[0] contains a slash it's a path; builtins don't.
		// More robustly: rewrite any arg that starts with "/" and is not already
		// under SandboxRoot — this handles cases like `find /workspace -name ...`
		if cfg.SandboxRoot != "" {
			rewritten := make([]string, len(args))
			copy(rewritten, args)
			for i := 1; i < len(rewritten); i++ {
				a := rewritten[i]
				if strings.HasPrefix(a, "/") &&
					!strings.HasPrefix(a, cfg.SandboxRoot) {
					rewritten[i] = pathmap.VirtualToReal(cfg.SandboxRoot, a)
				}
			}
			return rewritten, nil
		}

		return args, nil
	}
}
```

## Key design notes

- `CallHandlerFunc` receives args AFTER variable expansion. So `$HOME` in a script
  becomes `/home/user` (virtual) before this function sees it. The path rewriting
  handles this correctly.

- Block list uses prefix matching on the joined args string. For stricter matching,
  callers can supply exact commands like `"rm"` to block all `rm` invocations, or
  `"rm -rf /"` to block only that specific invocation.

- This fires for shell builtins too (`cd`, `echo`, `export`, etc.). The audit log
  will therefore log ALL commands, giving full visibility into what the agent does.

- Returning `interp.NewExitStatus(1)` from CallHandler aborts the current command
  with exit code 1 but does NOT halt the entire script (unless `set -e` is active).
