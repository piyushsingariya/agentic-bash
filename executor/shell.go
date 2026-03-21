package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/piyushsingariya/agentic-bash/internal/limitwriter"
	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
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
	execMiddlewares []func(next ExecHandlerFunc) ExecHandlerFunc
	openHandler     OpenHandlerFunc
	callHandler     interp.CallHandlerFunc
	statHandler     interp.StatHandlerFunc
	readDirHandler  interp.ReadDirHandlerFunc2

	// outputLimitBytes caps combined stdout+stderr per Run(); 0 = no cap.
	outputLimitBytes int64

	// sandboxRoot is the real on-disk temp directory. When non-empty, virtual
	// path translation is active: PWD is forced to the virtual path in env,
	// ExtractState returns the virtual cwd, and a cd() override is injected
	// into the preamble so that `cd /workspace` resolves inside the sandbox.
	sandboxRoot string
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

// WithExecMiddlewares sets the ExecHandlers middleware chain applied on each Run().
// Middlewares are chained first-to-last; the last one calls the real OS exec.
func (e *ShellExecutor) WithExecMiddlewares(mws ...func(next ExecHandlerFunc) ExecHandlerFunc) {
	e.execMiddlewares = mws
}

// WithExecHandler is a convenience wrapper that adapts a single ExecHandlerFunc
// (legacy style) into the middleware chain. The handler is treated as terminal —
// it must not call next.
func (e *ShellExecutor) WithExecHandler(h ExecHandlerFunc) {
	e.execMiddlewares = []func(next ExecHandlerFunc) ExecHandlerFunc{
		func(_ ExecHandlerFunc) ExecHandlerFunc { return h },
	}
}

// WithCallHandler sets the CallHandlerFunc called for every command (incl. builtins).
func (e *ShellExecutor) WithCallHandler(h interp.CallHandlerFunc) {
	e.callHandler = h
}

// WithStatHandler sets the StatHandlerFunc used by shell conditionals ([[ -f path ]]).
func (e *ShellExecutor) WithStatHandler(h interp.StatHandlerFunc) {
	e.statHandler = h
}

// WithReadDirHandler sets the ReadDirHandlerFunc2 used during glob expansion.
func (e *ShellExecutor) WithReadDirHandler(h interp.ReadDirHandlerFunc2) {
	e.readDirHandler = h
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

// WithSandboxRoot enables virtual path translation. root is the real on-disk
// temp directory. The shell's dir is expected to be a real path under root,
// but $PWD and the cwd returned by ExtractState will be the virtual path.
func (e *ShellExecutor) WithSandboxRoot(root string) {
	e.sandboxRoot = root
}

// Run implements Executor. The env and dir parameters are ignored; the
// executor manages state internally. Call ExtractState() to read updated
// state after Run() returns.
func (e *ShellExecutor) Run(ctx context.Context, cmd string, _ []string, _ string) Result {
	var stdout, stderr bytes.Buffer
	exitCode, retErr := e.runCore(ctx, cmd, &stdout, &stderr)
	return Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Error:    retErr,
	}
}

// RunToWriters executes cmd, writing stdout and stderr directly to the
// provided writers instead of buffering. Used by sandbox.RunStream to
// deliver output incrementally as the process produces it.
func (e *ShellExecutor) RunToWriters(ctx context.Context, cmd string, outW, errW io.Writer) (int, error) {
	return e.runCore(ctx, cmd, outW, errW)
}

// runCore is the shared implementation for Run and RunToWriters.
// It parses and executes cmd, writing output to outW and errW.
func (e *ShellExecutor) runCore(ctx context.Context, cmd string, outW, errW io.Writer) (int, error) {
	if strings.TrimSpace(cmd) == "" {
		return 0, nil
	}

	// Build the full script: function preamble + user command.
	fullCmd := e.buildPreamble() + cmd

	f, err := e.parser.Parse(strings.NewReader(fullCmd), "input")
	if err != nil {
		_, _ = io.WriteString(errW, "shell parse error: "+err.Error())
		return 1, err
	}

	// Output cap: wrap writers with limit writers that cancel the runner
	// context when the combined byte count exceeds the cap.
	runCtx := ctx
	if e.outputLimitBytes > 0 {
		cancelCtx, cancel := context.WithCancel(ctx)
		runCtx = cancelCtx
		outW, errW = limitwriter.NewPair(outW, errW, e.outputLimitBytes, cancel)
	}

	opts := []interp.RunnerOption{
		interp.Env(expand.ListEnviron(e.effectiveEnv()...)),
		interp.StdIO(nil, outW, errW),
	}
	if e.dir != "" {
		opts = append(opts, interp.Dir(e.dir))
	}
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
	if e.openHandler != nil {
		opts = append(opts, interp.OpenHandler(e.openHandler))
	}

	runner, err := interp.New(opts...)
	if err != nil {
		_, _ = io.WriteString(errW, err.Error())
		return 1, err
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

	return exitCode, retErr
}

// ExtractState implements StateExtractor.
// It returns the current exported environment and working directory.
// When sandboxRoot is set, the returned cwd is the virtual path (e.g. /home/user)
// rather than the real on-disk path.
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

	cwd = e.dir
	if e.sandboxRoot != "" {
		cwd = pathmap.RealToVirtual(e.sandboxRoot, e.dir)
	}
	return exported, cwd
}

// ResetState implements StateExtractor.
// It discards all accumulated session state and reinitialises to the given env/cwd.
// cwd may be a virtual path; when sandboxRoot is set it is translated to real.
func (e *ShellExecutor) ResetState(env map[string]string, cwd string) error {
	pairs := make([]string, 0, len(env))
	for k, v := range env {
		pairs = append(pairs, k+"="+v)
	}
	e.baseEnv = pairs
	realCwd := cwd
	if e.sandboxRoot != "" {
		realCwd = pathmap.VirtualToReal(e.sandboxRoot, cwd)
	}
	e.initDir = realCwd
	e.dir = realCwd
	e.vars = make(map[string]expand.Variable)
	e.funcs = make(map[string]*syntax.Stmt)
	return nil
}

// effectiveEnv returns the merged KEY=VALUE slice that will be passed to the
// next runner: base env overridden by any exported vars accumulated so far.
// When sandboxRoot is set, PWD is forced to the virtual path so that $PWD
// always shows the agent-visible path rather than the real tmpdir path.
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

	// Force PWD to the virtual path so $PWD looks right inside the sandbox.
	if e.sandboxRoot != "" {
		m["PWD"] = pathmap.RealToVirtual(e.sandboxRoot, e.dir)
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
//  1. A cd() override (when sandboxRoot is set) that translates virtual absolute
//     paths to real on-disk paths, giving the agent a Linux-feel `cd`.
//  2. Non-exported local variables (re-assigned verbatim).
//  3. Shell function definitions (printed from their AST nodes).
//
// Exported variables are instead injected via interp.Env() so they are
// visible to subprocesses; they do not need to appear in the preamble.
func (e *ShellExecutor) buildPreamble() string {
	var buf strings.Builder

	// Inject a cd() shim that makes `cd /virtual/path` work inside the sandbox
	// by prepending the real sandbox root before calling the builtin cd, then
	// updating $PWD to the virtual path so the agent always sees clean paths.
	if e.sandboxRoot != "" {
		root := e.sandboxRoot
		// single-quote the root so no shell expansion occurs; MkdirTemp paths
		// never contain single quotes.
		fmt.Fprintf(&buf,
			"cd() {\n"+
				"  local _prev=\"${PWD}\" _t=\"${1:-${HOME}}\"\n"+
				"  case \"${_t}\" in\n"+
				"    -)   command cd '%s'\"${OLDPWD}\" || return $?; local _tmp=\"${PWD}\"; export PWD=\"${OLDPWD}\"; export OLDPWD=\"${_tmp}\" ;;\n"+
				"    /*)  command cd '%s'\"${_t}\" || return $?; export OLDPWD=\"${_prev}\"; export PWD=\"${_t}\" ;;\n"+
				"    *)   command cd \"${_t}\" || return $?; local _real; _real=$(command pwd); _real=\"${_real#'%s'}\"; export OLDPWD=\"${_prev}\"; export PWD=\"${_real:-/}\" ;;\n"+
				"  esac\n"+
				"}\n",
			root, root, root,
		)
	}

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
	// The injected cd() shim is excluded: it is always rebuilt in buildPreamble
	// so we don't want it persisted (and possibly double-emitted) via e.funcs.
	if e.sandboxRoot != "" {
		e.funcs = make(map[string]*syntax.Stmt, len(r.Funcs))
		for name, stmt := range r.Funcs {
			if name == "cd" {
				continue
			}
			e.funcs[name] = stmt
		}
	} else {
		e.funcs = r.Funcs
	}
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
