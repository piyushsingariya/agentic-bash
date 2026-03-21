package intercept

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"mvdan.cc/sh/v3/interp"

	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
)

// CallConfig configures the CallHandler behaviour.
type CallConfig struct {
	// AuditWriter receives a log line for every command invocation (incl. builtins).
	// Nil disables audit logging.
	AuditWriter io.Writer

	// BlockList is a list of command patterns to deny unconditionally.
	// Each entry is prefix-matched against the full args joined by spaces.
	// Example: []string{"rm -rf /", "mkfs", "dd if=/dev/"}
	BlockList []string

	// SandboxRoot enables virtual→real path rewriting in args for external
	// commands. Any argument starting with "/" that is not already under
	// SandboxRoot is translated to its real on-disk path.
	SandboxRoot string
}

// NewCallHandler returns a CallHandlerFunc that fires for every command
// (including shell builtins and functions) and:
//  1. Logs the invocation to CallConfig.AuditWriter (if set).
//  2. Denies commands matching CallConfig.BlockList with exit code 1.
//  3. Rewrites virtual absolute path arguments to real paths.
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
				return nil, interp.NewExitStatus(1)
			}
		}

		// ── 3. Virtual path arg rewriting ────────────────────────────────────
		// Rewrite virtual absolute paths (e.g. /home/user) to real tmpdir paths.
		// Skip host-passthrough paths: /dev/, /proc/, /sys/ — these include
		// process-substitution pipes (/dev/fd/63) and must not be translated.
		if cfg.SandboxRoot != "" {
			rewritten := make([]string, len(args))
			copy(rewritten, args)
			for i := 1; i < len(rewritten); i++ {
				a := rewritten[i]
				if strings.HasPrefix(a, "/") &&
					!strings.HasPrefix(a, cfg.SandboxRoot) &&
					!isHostPath(a) &&
					!hostPathExists(a) {
					rewritten[i] = pathmap.VirtualToReal(cfg.SandboxRoot, a)
				}
			}
			return rewritten, nil
		}

		return args, nil
	}
}

// isHostPath returns true for kernel/device paths that must never be translated.
func isHostPath(p string) bool {
	return strings.HasPrefix(p, "/dev/") ||
		strings.HasPrefix(p, "/proc/") ||
		strings.HasPrefix(p, "/sys/")
}

// hostPathExists returns true if p exists on the real host filesystem as-is.
// Used to skip rewriting process-substitution temp files (e.g. macOS uses
// /private/var/folders/... for <(...) FIFOs) and other real host paths.
func hostPathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
