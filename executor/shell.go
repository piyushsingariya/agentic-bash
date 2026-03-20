package executor

import (
	"bytes"
	"context"
	"io"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/piyushsingariya/agentic-bash/internal/limitwriter"
)

// ExecHandlerFunc is an alias for interp.ExecHandlerFunc. Provided so callers
// can import only this package when wiring Phase 6 package-manager shims.
type ExecHandlerFunc = interp.ExecHandlerFunc

// OpenHandlerFunc is an alias for interp.OpenHandlerFunc. Provided so callers
// can import only this package when wiring Phase 3 virtual-filesystem hooks.
type OpenHandlerFunc = interp.OpenHandlerFunc

// ShellExecutor runs commands using the mvdan.cc/sh pure Go shell interpreter.
//
// Unlike NativeExecutor, no host shell (/bin/bash, /bin/sh) is required for
// built-in operations. Pipelines, redirections, variables, loops, functions,
// and arithmetic all execute in-process.
//
// # State persistence
//
// ShellExecutor implements StateExtractor. Session state is carried forward
// across Run() calls as follows:
//
//   - Exported variables  → injected into the next runner's base environment.
//   - Working directory   → applied via interp.Dir on the next runner.
//   - Shell functions     → printed back to source and prepended as a preamble
//     to the next command so they are re-defined before execution.
//
// Non-exported (local) variables are intentionally scoped to a single Run()
// call, matching POSIX semantics for separate shell invocations.
//
// # Hook points for later phases
//
// WithExecHandler wires Phase 6 package-manager shims.
// WithOpenHandler wires Phase 3 virtual-filesystem routing.
type ShellExecutor struct {
	parser  *syntax.Parser
	printer *syntax.Printer

	// Accumulated session state (updated after every Run()).
	vars  map[string]expand.Variable // exported vars from previous runs
	dir   string                     // current working directory
	funcs map[string]*syntax.Stmt    // function definitions from previous runs

	// Initial values — used by ResetState().
	baseEnv []string
	initDir string

	// Optional hook points (nil = default OS behaviour).
	execHandler ExecHandlerFunc
	openHandler OpenHandlerFunc

	// outputLimitBytes caps combined stdout+stderr per Run(); 0 = no cap.
	outputLimitBytes int64
}

// NewShellExecutor creates a ShellExecutor initialised with the given
// environment (KEY=VALUE pairs) and working directory.
func NewShellExecutor(env []string, cwd string) *ShellExecutor {
	return &ShellExecutor{
		parser:  syntax.NewParser(syntax.Variant(syntax.LangBash)),
		printer: syntax.NewPrinter(),
		vars:    make(map[string]expand.Variable),
		funcs:   make(map[string]*syntax.Stmt),
		dir:     cwd,
		baseEnv: env,
		initDir: cwd,
	}
}

// WithExecHandler replaces the default OS exec with a custom handler.
// Intended for Phase 6 package-manager interception.
func (e *ShellExecutor) WithExecHandler(h ExecHandlerFunc) {
	e.execHandler = h
}

// WithOpenHandler replaces the default OS file open with a custom handler.
// Intended for Phase 3 virtual-filesystem routing.
func (e *ShellExecutor) WithOpenHandler(h OpenHandlerFunc) {
	e.openHandler = h
}

// WithOutputLimit sets a combined stdout+stderr cap (in bytes) applied on
// each Run() call.  When the cap is exceeded the runner context is cancelled,
// stopping execution cleanly.  Zero disables the cap.
func (e *ShellExecutor) WithOutputLimit(maxBytes int64) {
	e.outputLimitBytes = maxBytes
}

// Run implements Executor. The env and dir parameters are ignored; the
// executor manages state internally. Call ExtractState() to read updated
// state after Run() returns.
func (e *ShellExecutor) Run(ctx context.Context, cmd string, _ []string, _ string) Result {
	if strings.TrimSpace(cmd) == "" {
		return Result{}
	}

	// Build the full script: function preamble + user command.
	fullCmd := e.buildPreamble() + cmd

	f, err := e.parser.Parse(strings.NewReader(fullCmd), "input")
	if err != nil {
		return Result{
			Stderr:   "shell parse error: " + err.Error(),
			ExitCode: 1,
			Error:    err,
		}
	}

	var stdout, stderr bytes.Buffer

	// Output cap: wrap buffers with limit writers that cancel the runner
	// context when the combined byte count exceeds the cap.
	runCtx := ctx
	var outW, errW io.Writer = &stdout, &stderr
	if e.outputLimitBytes > 0 {
		cancelCtx, cancel := context.WithCancel(ctx)
		runCtx = cancelCtx
		outW, errW = limitwriter.NewPair(&stdout, &stderr, e.outputLimitBytes, cancel)
	}

	opts := []interp.RunnerOption{
		interp.Env(expand.ListEnviron(e.effectiveEnv()...)),
		interp.StdIO(nil, outW, errW),
	}
	if e.dir != "" {
		opts = append(opts, interp.Dir(e.dir))
	}
	if e.execHandler != nil {
		opts = append(opts, interp.ExecHandler(e.execHandler))
	}
	if e.openHandler != nil {
		opts = append(opts, interp.OpenHandler(e.openHandler))
	}

	runner, err := interp.New(opts...)
	if err != nil {
		return Result{Stderr: err.Error(), ExitCode: 1, Error: err}
	}

	runErr := runner.Run(runCtx, f)

	// Sync state from the completed runner.
	e.syncFrom(runner)

	exitCode := resolveExitCode(ctx, runErr)
	var retErr error
	if ctx.Err() != nil {
		retErr = ctx.Err()
	} else if runErr != nil {
		if _, isExit := interp.IsExitStatus(runErr); !isExit {
			retErr = runErr
		}
	}

	return Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Error:    retErr,
	}
}

// ExtractState implements StateExtractor.
// It returns the current exported environment and working directory.
func (e *ShellExecutor) ExtractState() (env map[string]string, cwd string) {
	exported := make(map[string]string)

	// Base env entries (all exported by definition as KEY=VALUE).
	for _, pair := range e.baseEnv {
		if i := strings.IndexByte(pair, '='); i > 0 {
			exported[pair[:i]] = pair[i+1:]
		}
	}

	// Apply accumulated variable changes.
	for name, vr := range e.vars {
		if !vr.IsSet() {
			delete(exported, name) // variable was unset
		} else if vr.Exported && vr.Kind == expand.String {
			exported[name] = vr.Str
		} else if !vr.Exported {
			// Non-exported: remove from the env view (but it lives in the preamble).
			delete(exported, name)
		}
	}

	return exported, e.dir
}

// ResetState implements StateExtractor.
// It discards all accumulated session state and reinitialises to the given env/cwd.
func (e *ShellExecutor) ResetState(env map[string]string, cwd string) error {
	pairs := make([]string, 0, len(env))
	for k, v := range env {
		pairs = append(pairs, k+"="+v)
	}
	e.baseEnv = pairs
	e.initDir = cwd
	e.dir = cwd
	e.vars = make(map[string]expand.Variable)
	e.funcs = make(map[string]*syntax.Stmt)
	return nil
}

// effectiveEnv returns the merged KEY=VALUE slice that will be passed to the
// next runner: base env overridden by any exported vars accumulated so far.
func (e *ShellExecutor) effectiveEnv() []string {
	m := make(map[string]string)

	for _, pair := range e.baseEnv {
		if i := strings.IndexByte(pair, '='); i > 0 {
			m[pair[:i]] = pair[i+1:]
		}
	}
	for name, vr := range e.vars {
		if !vr.IsSet() {
			delete(m, name)
		} else if vr.Exported && vr.Kind == expand.String {
			m[name] = vr.Str
		}
	}

	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// buildPreamble constructs the shell source that re-establishes session state
// at the top of each Run() call:
//
//  1. Non-exported local variables (re-assigned verbatim).
//  2. Shell function definitions (printed from their AST nodes).
//
// Exported variables are instead injected via interp.Env() so they are
// visible to subprocesses; they do not need to appear in the preamble.
func (e *ShellExecutor) buildPreamble() string {
	if len(e.vars) == 0 && len(e.funcs) == 0 {
		return ""
	}

	var buf strings.Builder

	// Non-exported, non-read-only local variables.
	// ReadOnly variables (e.g. UID, EUID, GID) must be skipped: in a
	// non-interactive shell, assigning to a read-only variable is a fatal
	// error and would abort the script before any function definitions run.
	for name, vr := range e.vars {
		if vr.IsSet() && !vr.Exported && !vr.ReadOnly && vr.Kind == expand.String {
			buf.WriteString(name)
			buf.WriteByte('=')
			buf.WriteString(singleQuote(vr.Str))
			buf.WriteByte('\n')
		}
	}

	// Function definitions: runner.Funcs stores the body node keyed by name,
	// so we must prepend "name() " to reconstruct the full declaration.
	for name, stmt := range e.funcs {
		buf.WriteString(name)
		buf.WriteString("() ")
		if err := e.printer.Print(&buf, stmt); err == nil {
			buf.WriteByte('\n')
		}
	}

	return buf.String()
}

// syncFrom extracts the runner's final state (vars, dir, funcs) and merges it
// into the ShellExecutor's accumulated session state.
func (e *ShellExecutor) syncFrom(r *interp.Runner) {
	// Merge variables: entries from Vars override the accumulated state.
	for name, vr := range r.Vars {
		if !vr.IsSet() {
			delete(e.vars, name)
		} else {
			e.vars[name] = vr
		}
	}

	// Update working directory.
	e.dir = r.Dir

	// Replace function table with the runner's final view. This naturally
	// handles both newly-defined and deleted functions (unset -f).
	e.funcs = r.Funcs
}

// resolveExitCode converts a runner.Run() error into an integer exit code.
func resolveExitCode(ctx context.Context, err error) int {
	if err == nil {
		return 0
	}
	if status, ok := interp.IsExitStatus(err); ok {
		return int(status)
	}
	if ctx.Err() != nil {
		return 1
	}
	return 1
}

// singleQuote wraps s in POSIX single quotes, escaping any embedded ' characters.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
