package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/piyushsingariya/agentic-bash/executor"
	sbfs "github.com/piyushsingariya/agentic-bash/fs"
	"github.com/piyushsingariya/agentic-bash/internal/cgroups"
	"github.com/piyushsingariya/agentic-bash/isolation"
)

// DefaultTimeout is applied when Options.Limits.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// Sandbox is the main entry point. It provides a stateful, isolated execution
// environment for AI agents. Each Run() call shares the same shell state
// (environment variables, working directory) as previous calls within the
// same Sandbox, matching the stateful session model of just-bash.
//
// The default executor is ShellExecutor (mvdan.cc/sh pure Go interpreter).
// NativeExecutor (Phase 1 /bin/sh subprocess) is retained as a fallback and
// is used automatically if no bash-compatible shell features are needed.
type Sandbox struct {
	opts      Options
	state     *ShellState
	exec      executor.Executor
	fs        *sbfs.LayeredFS
	tracker   *sbfs.ChangeTracker
	isolation isolation.IsolationStrategy
	cgroupMgr cgroups.Manager        // Phase 5: per-sandbox cgroup factory
	metrics   *isolation.ExecMetrics // Phase 5: reset each Run(); read after
	tempDir   string                 // owned exclusively; removed on Close()
	closed    bool
	ctx       context.Context    // base context; replaced via SetContext
	cancelCtx context.CancelFunc // cancels ctx; called on Close
}

// New creates and initialises a Sandbox with the provided options.
// Callers must call Close() when done to release the sandbox's temp directory.
//
// The default executor is ShellExecutor (pure Go, no host shell required).
// Pass executor.NewNativeExecutor() via an option if you need the Phase 1
// /bin/sh subprocess behaviour.
func New(opts Options) (*Sandbox, error) {
	tmpDir, err := os.MkdirTemp("", "agentic-bash-*")
	if err != nil {
		return nil, fmt.Errorf("sandbox: create temp dir: %w", err)
	}

	// Default the working directory to the sandbox's own temp dir so commands
	// have a safe, writable home without touching the host filesystem.
	if opts.WorkDir == "" {
		opts.WorkDir = tmpDir
	}

	// Ensure the configured WorkDir actually exists.
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("sandbox: create work dir %q: %w", opts.WorkDir, err)
	}

	if opts.Limits.Timeout == 0 {
		opts.Limits.Timeout = DefaultTimeout
	}

	state := newShellState(opts)

	// Phase 3: layered in-memory filesystem with optional read-only base layer.
	lfs := sbfs.NewLayeredFS(tmpDir, opts.BaseImageDir)
	tracker := sbfs.NewChangeTracker(lfs)

	// Phase 2: ShellExecutor is the default — pure Go shell, state managed
	// in-process, no temp-file captures needed.
	shellExec := executor.NewShellExecutor(state.EnvSlice(), state.Cwd)

	// Phase 4: select isolation strategy and wire a custom ExecHandler so that
	// every external command spawned by the shell runs with isolation applied.
	iso := isolation.SelectStrategy(opts.Isolation)

	baseCtx, cancelCtx := context.WithCancel(context.Background())

	s := &Sandbox{
		opts:      opts,
		state:     state,
		isolation: iso,
		exec:      shellExec,
		fs:        lfs,
		tracker:   tracker,
		tempDir:   tmpDir,
		cgroupMgr: cgroups.NewManager(), // Phase 5: no-op on non-Linux
		metrics:   &isolation.ExecMetrics{},
		ctx:       baseCtx,
		cancelCtx: cancelCtx,
	}
	// Wire the open handler once — s.tracker and s.tempDir are stable for the
	// sandbox lifetime, so there is no need to re-wire it on every Run().
	shellExec.WithOpenHandler(sbfs.NewOpenHandler(s.tracker, s.tempDir))
	s.wireHandlers()
	return s, nil
}

// SetContext replaces the base context used by all future Run / RunContext calls.
// Cancelling the provided context is equivalent to calling Close() from outside
// the sandbox — all in-flight and future commands will fail with a context error.
// The sandbox does NOT take ownership of the context's cancel function.
func (s *Sandbox) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// RunContext executes cmd inside the sandbox, honouring both the caller-supplied
// context and the configured per-run timeout. Whichever deadline arrives first
// wins, so the caller can cancel individual commands via Ctrl+C without
// affecting the sandbox's base context.
//
// This is the preferred entry point for interactive and concurrent callers.
// Run() is a convenience wrapper around RunContext(s.ctx, cmd).
func (s *Sandbox) RunContext(ctx context.Context, cmd string) ExecutionResult {
	// Merge the caller context with the sandbox's base context so that either
	// cancellation kills the run.
	merged, mergedCancel := context.WithCancel(ctx)
	defer mergedCancel()

	// Propagate sandbox-level cancellation into the merged context.
	go func() {
		select {
		case <-s.ctx.Done():
			mergedCancel()
		case <-merged.Done():
		}
	}()

	return s.run(merged, cmd)
}

// Run executes cmd inside the sandbox shell and returns the result.
//
// When using ShellExecutor (default):
//   - Exported variables persist to the next Run() via the in-process interpreter.
//   - Working directory changes (cd) persist to the next Run().
//   - Shell function definitions persist to the next Run().
//
// When using NativeExecutor (Phase 1 fallback):
//   - State is captured via a shell trap that writes env/cwd to temp files.
func (s *Sandbox) Run(cmd string) ExecutionResult {
	return s.RunContext(s.ctx, cmd)
}

// run is the internal implementation shared by Run and RunContext.
func (s *Sandbox) run(ctx context.Context, cmd string) ExecutionResult {
	if s.closed {
		return ExecutionResult{ExitCode: 1, Error: fmt.Errorf("sandbox is closed")}
	}

	if s.opts.OnCommand != nil {
		s.opts.OnCommand(cmd)
	}

	// Reset the per-run filesystem change tracker and resource metrics.
	s.tracker.Reset()
	s.metrics = &isolation.ExecMetrics{}
	// Re-wire handlers so the exec handler captures the fresh metrics pointer.
	s.wireHandlers()

	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, s.opts.Limits.Timeout)
	defer cancel()

	var res executor.Result

	if se, ok := s.exec.(executor.StateExtractor); ok {
		// ShellExecutor path: state is managed entirely in-process.
		// Pass the raw command; env and dir params are ignored by ShellExecutor.
		res = s.exec.Run(ctx, cmd, nil, "")

		// Sync ShellState from the interpreter's post-run state.
		env, cwd := se.ExtractState()
		s.state.Env = env
		s.state.Cwd = cwd
	} else {
		// NativeExecutor path (Phase 1 fallback): wrap command to capture state.
		stateDir, err := os.MkdirTemp(s.tempDir, "run-*")
		if err != nil {
			return ExecutionResult{ExitCode: 1, Error: fmt.Errorf("sandbox: create run dir: %w", err)}
		}
		defer os.RemoveAll(stateDir)

		envFile := filepath.Join(stateDir, "env")
		cwdFile := filepath.Join(stateDir, "cwd")

		wrapped := buildWrappedCommand(cmd, s.state.Cwd, envFile, cwdFile)
		res = s.exec.Run(ctx, wrapped, s.state.EnvSlice(), s.state.Cwd)

		// Parse state from captured files; silently retain prior state on miss
		// (e.g. when the command timed out before the trap could fire).
		if newEnv, err := parseEnvFile(envFile); err == nil {
			s.state.Env = newEnv
		}
		if data, err := os.ReadFile(cwdFile); err == nil {
			if cwd := strings.TrimSpace(string(data)); cwd != "" {
				s.state.Cwd = cwd
			}
		}
	}

	duration := time.Since(start)
	s.state.History = append(s.state.History, cmd)

	result := ExecutionResult{
		Stdout:        res.Stdout,
		Stderr:        res.Stderr,
		ExitCode:      res.ExitCode,
		Duration:      duration,
		Error:         res.Error,
		FilesCreated:  s.tracker.FilesCreated(),
		FilesModified: s.tracker.FilesModified(),
		FilesDeleted:  s.tracker.FilesDeleted(),
		// Phase 5: resource usage from cgroup accounting (Linux only).
		CPUTime:      time.Duration(s.metrics.CPUUsec) * time.Microsecond,
		MemoryPeakMB: int(s.metrics.MemPeakBytes / (1024 * 1024)),
	}

	if s.opts.OnResult != nil {
		s.opts.OnResult(result)
	}

	return result
}

// FS returns the sandbox's LayeredFS, enabling callers to call
// sbfs.Snapshot / sbfs.Restore for point-in-time filesystem checkpoints.
func (s *Sandbox) FS() *sbfs.LayeredFS { return s.fs }

// Isolation returns the active IsolationStrategy.
func (s *Sandbox) Isolation() isolation.IsolationStrategy { return s.isolation }

// State returns a read-only view of the current ShellState. The returned
// pointer is valid until the next call to Run() or Reset().
func (s *Sandbox) State() *ShellState {
	return s.state
}

// wireHandlers re-wires the exec handler to capture the current metrics
// pointer.  Called from New() (after the open handler is set once) and the
// start of each Run() so that a fresh *ExecMetrics is captured by the closure.
func (s *Sandbox) wireHandlers() {
	shellExec, ok := s.exec.(*executor.ShellExecutor)
	if !ok {
		return
	}

	// Phase 5: output cap for the in-process shell.
	if s.opts.Limits.MaxOutputMB > 0 {
		shellExec.WithOutputLimit(int64(s.opts.Limits.MaxOutputMB) * 1024 * 1024)
	}

	limits := isolation.ExecLimits{
		MaxOutputBytes: int64(s.opts.Limits.MaxOutputMB) * 1024 * 1024,
		CgroupManager:  s.cgroupMgr,
		MaxMemoryBytes: int64(s.opts.Limits.MaxMemoryMB) * 1024 * 1024,
		CPUQuota:       s.opts.Limits.MaxCPUPercent / 100.0,
	}
	shellExec.WithExecHandler(isolation.NewIsolatedExecHandler(s.isolation, limits, s.metrics))
}

// Reset restores the sandbox to its initial Options state.
// Any env changes, cwd changes, history, and function definitions from
// previous Run() calls are discarded.
func (s *Sandbox) Reset() {
	s.state = newShellState(s.opts)

	// Clear files created during previous runs; keep the root directory.
	_ = s.fs.Clear()
	s.tracker.Reset()

	if se, ok := s.exec.(executor.StateExtractor); ok {
		_ = se.ResetState(s.state.Env, s.state.Cwd)
	}
	s.metrics = &isolation.ExecMetrics{}
	s.wireHandlers()
}

// Close releases all resources owned by this Sandbox, including its temp
// directory and base context. Further calls to Run() will return an error.
// Calling Close() more than once is safe.
func (s *Sandbox) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cancelCtx != nil {
		s.cancelCtx()
	}
	return os.RemoveAll(s.tempDir)
}

// buildWrappedCommand constructs a /bin/sh script used by NativeExecutor to
// capture final session state via a trap on EXIT.
// Retained for the NativeExecutor fallback path; not used by ShellExecutor.
func buildWrappedCommand(cmd, cwd, envFile, cwdFile string) string {
	return fmt.Sprintf(
		`set -a
trap '__ec=$?; env > %s 2>/dev/null; pwd > %s 2>/dev/null; exit $__ec' EXIT
cd %s || exit 1
%s`,
		shellQuote(envFile),
		shellQuote(cwdFile),
		shellQuote(cwd),
		cmd,
	)
}

// shellQuote wraps s in single quotes and escapes any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
