package sandbox

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/piyushsingariya/agentic-bash/executor"
	"github.com/piyushsingariya/agentic-bash/executor/intercept"
	sbfs "github.com/piyushsingariya/agentic-bash/fs"
	"github.com/piyushsingariya/agentic-bash/internal/cgroups"
	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
	"github.com/piyushsingariya/agentic-bash/isolation"
	"github.com/piyushsingariya/agentic-bash/network"
	"github.com/piyushsingariya/agentic-bash/packages"
)

// DefaultTimeout is applied when Options.Limits.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// Sandbox is the main entry point. It provides a stateful, isolated execution
// environment for AI agents. Each Run() call shares the same shell state
// (environment variables, working directory) as previous calls within the
// same Sandbox. Unlike just-bash, which resets all state (env, cwd, functions)
// after every exec(), agentic-bash deliberately persists state across Run() calls.
//
// The default executor is ShellExecutor (mvdan.cc/sh pure Go interpreter).
// NativeExecutor (Phase 1 /bin/sh subprocess) is retained as a fallback and
// is used automatically if no bash-compatible shell features are needed.
type Sandbox struct {
	opts       Options
	state      *ShellState
	exec       executor.Executor
	fs         *sbfs.LayeredFS
	tracker    *sbfs.ChangeTracker
	isolation  isolation.IsolationStrategy
	cgroupMgr  cgroups.Manager        // Phase 5: per-sandbox cgroup factory
	metrics    *isolation.ExecMetrics // Phase 5: reset each Run(); read after
	manifest   *packages.Manifest     // Phase 6: tracks installed packages
	pythonWASM *packages.PythonWASMShim // non-nil when PythonRuntimeWASM is set
	tempDir    string                 // owned exclusively; removed on Close()
	closed     bool
	ctx        context.Context    // base context; replaced via SetContext
	cancelCtx  context.CancelFunc // cancels ctx; called on Close
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

	// Apply BootstrapConfig defaults.
	if opts.Bootstrap.UserName == "" {
		opts.Bootstrap.UserName = "user"
	}
	if opts.Bootstrap.Hostname == "" {
		opts.Bootstrap.Hostname = "sandbox"
	}
	if opts.Bootstrap.UID == 0 {
		opts.Bootstrap.UID = 1000
	}
	if opts.Bootstrap.GID == 0 {
		opts.Bootstrap.GID = 1000
	}

	// Always bootstrap the Linux skeleton so virtual paths (etc, home, …) exist.
	if err := bootstrapFS(tmpDir, opts.Bootstrap); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("sandbox: bootstrap fs: %w", err)
	}

	// Default WorkDir: virtual home for the Linux preset, real tmpDir otherwise.
	if opts.WorkDir == "" {
		if opts.EnvPreset == EnvPresetLinux {
			opts.WorkDir = "/home/" + opts.Bootstrap.UserName
		} else {
			opts.WorkDir = tmpDir
		}
	}

	// Ensure the real counterpart of WorkDir exists on disk.
	realWorkDir := pathmap.VirtualToReal(tmpDir, opts.WorkDir)
	if err := os.MkdirAll(realWorkDir, 0o755); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("sandbox: create work dir %q: %w", opts.WorkDir, err)
	}

	if opts.Limits.Timeout == 0 {
		opts.Limits.Timeout = DefaultTimeout
	}

	state := newShellState(opts)
	injectOverlayEnv(state, tmpDir)

	// Phase 3: layered in-memory filesystem with optional read-only base layer.
	lfs := sbfs.NewLayeredFS(tmpDir, opts.BaseImageDir)
	tracker := sbfs.NewChangeTracker(lfs)

	// Phase 2: ShellExecutor is the default — pure Go shell, state managed
	// in-process, no temp-file captures needed.
	// Pass the real initial dir; the virtual path is handled via sandboxRoot.
	shellExec := executor.NewShellExecutor(state.EnvSlice(), realWorkDir)
	shellExec.WithSandboxRoot(tmpDir)

	// Phase 4: select isolation strategy and wire a custom ExecHandler so that
	// every external command spawned by the shell runs with isolation applied.
	iso := isolation.SelectStrategy(opts.Isolation)

	// Python WASM runtime: compile the module once at sandbox creation so that
	// each Run() call can instantiate a fresh module cheaply.
	var pythonWASM *packages.PythonWASMShim
	if opts.PythonRuntime == PythonRuntimeWASM {
		if len(opts.PythonWASMBytes) == 0 {
			_ = os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("sandbox: PythonRuntimeWASM requires PythonWASMBytes (see packages.FetchPythonWASM)")
		}
		shim, shimErr := packages.NewPythonWASMShim(context.Background(), opts.PythonWASMBytes, tmpDir)
		if shimErr != nil {
			_ = os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("sandbox: init python wasm: %w", shimErr)
		}
		pythonWASM = shim
	}

	baseCtx, cancelCtx := context.WithCancel(context.Background())

	s := &Sandbox{
		opts:       opts,
		state:      state,
		isolation:  iso,
		exec:       shellExec,
		fs:         lfs,
		tracker:    tracker,
		tempDir:    tmpDir,
		cgroupMgr:  cgroups.NewManager(), // Phase 5: no-op on non-Linux
		metrics:    &isolation.ExecMetrics{},
		manifest:   packages.NewManifest(), // Phase 6
		pythonWASM: pythonWASM,
		ctx:        baseCtx,
		cancelCtx:  cancelCtx,
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

// Manifest returns the sandbox's package manifest, which tracks all packages
// installed via pip or apt during this sandbox session.
func (s *Sandbox) Manifest() *packages.Manifest {
	return s.manifest
}

// injectOverlayEnv prepends the overlay's Python site-packages dir to
// PYTHONPATH and the overlay's bin dir to PATH so that packages installed
// inside the sandbox are immediately importable / executable.
func injectOverlayEnv(state *ShellState, overlayRoot string) {
	pythonPath := packages.OverlayPythonPath(overlayRoot)
	if existing := state.Env["PYTHONPATH"]; existing != "" {
		state.Env["PYTHONPATH"] = pythonPath + ":" + existing
	} else {
		state.Env["PYTHONPATH"] = pythonPath
	}

	binPath := packages.OverlayBinPath(overlayRoot)
	if existing := state.Env["PATH"]; existing != "" {
		state.Env["PATH"] = binPath + ":" + existing
	} else {
		state.Env["PATH"] = binPath
	}
}

// wireHandlers assembles the full handler chain on shellExec. Called from New()
// and at the start of each Run() so that a fresh *ExecMetrics pointer is captured.
func (s *Sandbox) wireHandlers() {
	shellExec, ok := s.exec.(*executor.ShellExecutor)
	if !ok {
		return
	}

	// Output cap for the in-process shell.
	if s.opts.Limits.MaxOutputMB > 0 {
		shellExec.WithOutputLimit(int64(s.opts.Limits.MaxOutputMB) * 1024 * 1024)
	}

	// Network filter.
	var netFilter network.Filter
	switch s.opts.Network.Mode {
	case NetworkDeny:
		netFilter = network.NewDeny()
	case NetworkAllowlist:
		netFilter = network.NewAllowlist(s.opts.Network.Allowlist)
	default:
		netFilter = network.NewAllow()
	}

	limits := isolation.ExecLimits{
		MaxOutputBytes: int64(s.opts.Limits.MaxOutputMB) * 1024 * 1024,
		CgroupManager:  s.cgroupMgr,
		MaxMemoryBytes: int64(s.opts.Limits.MaxMemoryMB) * 1024 * 1024,
		CPUQuota:       s.opts.Limits.MaxCPUPercent / 100.0,
		NetworkFilter:  netFilter,
	}

	interceptCfg := intercept.Config{
		UserName:    s.opts.Bootstrap.UserName,
		Hostname:    s.opts.Bootstrap.Hostname,
		UID:         s.opts.Bootstrap.UID,
		GID:         s.opts.Bootstrap.GID,
		SandboxRoot: s.tempDir,
	}

	shimCfg := packages.ShimConfig{
		OverlayRoot: s.tempDir,
		Manifest:    s.manifest,
		CacheDir:    packages.DefaultCacheDir(),
	}

	// ExecHandlers middleware chain (outermost → innermost):
	//   1. Audit       — logs every external command spawn
	//   2. Dispatcher  — virtual command shims (sysinfo, filesystem, env)
	//   3. Shim        — package manager interception (pip, apt)
	//   4. Isolation   — resource limits + process isolation (terminal)
	allInterceptors := append(
		append(
			intercept.NewSysInfoInterceptors(interceptCfg),
			intercept.NewFilesystemInterceptors(interceptCfg)...,
		),
		intercept.NewEnvInterceptors(interceptCfg)...,
	)
	middlewares := []func(next executor.ExecHandlerFunc) executor.ExecHandlerFunc{
		intercept.NewAuditMiddleware(s.opts.AuditWriter),
		intercept.NewDispatcher(allInterceptors...),
		packages.NewShimMiddleware(shimCfg),
	}
	// PythonRuntimeWASM: intercept python3/python after pip shim (which handles
	// "python3 -m pip ...") so pip commands still redirect to the overlay while
	// all other Python invocations run inside the WASI sandbox.
	if s.pythonWASM != nil {
		middlewares = append(middlewares, packages.NewPythonWASMMiddleware(s.pythonWASM))
	}
	middlewares = append(middlewares, isolation.NewIsolatedExecMiddleware(s.isolation, limits, s.metrics))
	shellExec.WithExecMiddlewares(middlewares...)

	// CallHandler: audit log + block list + virtual path arg rewriting.
	shellExec.WithCallHandler(intercept.NewCallHandler(intercept.CallConfig{
		AuditWriter: s.opts.AuditWriter,
		BlockList:   s.opts.BlockList,
		SandboxRoot: s.tempDir,
	}))

	// StatHandler: fixes [[ -f /virtual/path ]] and similar conditionals.
	shellExec.WithStatHandler(intercept.NewStatHandler(s.tempDir))

	// ReadDirHandler2: fixes glob expansion over virtual paths.
	shellExec.WithReadDirHandler(intercept.NewReadDirHandler(s.tempDir))
}

// Reset restores the sandbox to its initial Options state.
// Any env changes, cwd changes, history, and function definitions from
// previous Run() calls are discarded.
func (s *Sandbox) Reset() {
	s.state = newShellState(s.opts)
	injectOverlayEnv(s.state, s.tempDir)

	// Clear files created during previous runs; keep the root directory.
	_ = s.fs.Clear()
	// Re-bootstrap the Linux skeleton so that /home/{user}, /etc, etc. exist
	// again after Clear() wiped them. Without this, interp.New() fails to stat
	// the working directory on the next Run().
	_ = bootstrapFS(s.tempDir, s.opts.Bootstrap)
	s.tracker.Reset()

	if se, ok := s.exec.(executor.StateExtractor); ok {
		// ResetState translates virtual cwd → real internally when sandboxRoot is set.
		_ = se.ResetState(s.state.Env, s.state.Cwd)
	}
	s.metrics = &isolation.ExecMetrics{}
	s.manifest = packages.NewManifest()
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
	if s.pythonWASM != nil {
		_ = s.pythonWASM.Close(context.Background())
	}
	return os.RemoveAll(s.tempDir)
}

// RunStream executes cmd inside the sandbox and writes stdout/stderr to the
// provided writers in real time as the process produces output.
// Returns the exit code and any infrastructure error (timeout, closed, etc.).
func (s *Sandbox) RunStream(ctx context.Context, cmd string, stdout, stderr io.Writer) (int, error) {
	if s.closed {
		return 1, fmt.Errorf("sandbox is closed")
	}
	if s.opts.OnCommand != nil {
		s.opts.OnCommand(cmd)
	}

	s.tracker.Reset()
	s.metrics = &isolation.ExecMetrics{}
	s.wireHandlers()

	merged, mergedCancel := context.WithCancel(ctx)
	defer mergedCancel()
	go func() {
		select {
		case <-s.ctx.Done():
			mergedCancel()
		case <-merged.Done():
		}
	}()

	start := time.Now()
	runCtx, runCancel := context.WithTimeout(merged, s.opts.Limits.Timeout)
	defer runCancel()

	var exitCode int
	var runErr error

	if se, ok := s.exec.(*executor.ShellExecutor); ok {
		exitCode, runErr = se.RunToWriters(runCtx, cmd, stdout, stderr)
		env, cwd := se.ExtractState()
		s.state.Env = env
		s.state.Cwd = cwd
	} else {
		// NativeExecutor fallback: buffer then copy.
		res := s.exec.Run(runCtx, cmd, s.state.EnvSlice(), s.state.Cwd)
		_, _ = io.WriteString(stdout, res.Stdout)
		_, _ = io.WriteString(stderr, res.Stderr)
		exitCode = res.ExitCode
		runErr = res.Error
	}

	s.state.History = append(s.state.History, cmd)

	if s.opts.OnResult != nil {
		s.opts.OnResult(ExecutionResult{
			ExitCode:      exitCode,
			Duration:      time.Since(start),
			Error:         runErr,
			FilesCreated:  s.tracker.FilesCreated(),
			FilesModified: s.tracker.FilesModified(),
			FilesDeleted:  s.tracker.FilesDeleted(),
			CPUTime:       time.Duration(s.metrics.CPUUsec) * time.Microsecond,
			MemoryPeakMB:  int(s.metrics.MemPeakBytes / (1024 * 1024)),
		})
	}

	return exitCode, runErr
}

// WriteFile writes data to path inside the sandbox filesystem.
// Relative paths are resolved against the current working directory.
func (s *Sandbox) WriteFile(path string, data []byte) error {
	return s.fs.WriteFile(s.resolvePath(path), data, 0o644)
}

// ReadFile reads and returns the contents of path inside the sandbox filesystem.
// Relative paths are resolved against the current working directory.
func (s *Sandbox) ReadFile(path string) ([]byte, error) {
	return s.fs.ReadFile(s.resolvePath(path))
}

// ListFiles returns the entries of dir inside the sandbox filesystem.
// Relative paths are resolved against the current working directory.
func (s *Sandbox) ListFiles(dir string) ([]FileInfo, error) {
	abs := s.resolvePath(dir)
	entries, err := s.fs.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	infos := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		fi, fiErr := e.Info()
		if fiErr != nil {
			continue
		}
		infos = append(infos, FileInfo{
			Name:    e.Name(),
			Path:    filepath.Join(abs, e.Name()),
			Size:    fi.Size(),
			Mode:    fi.Mode(),
			ModTime: fi.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	return infos, nil
}

// UploadTar extracts a tar archive from r into the sandbox filesystem.
// Existing files are overwritten; the sandbox root is not wiped first.
// Path traversal attempts (entries escaping the sandbox root) are rejected.
func (s *Sandbox) UploadTar(r io.Reader) error {
	root := s.fs.Root()
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("upload tar: read header: %w", err)
		}
		target := filepath.Join(root, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, root+string(filepath.Separator)) && target != root {
			return fmt.Errorf("upload tar: path escapes sandbox root: %s", hdr.Name)
		}
		info := hdr.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, info.Mode()); err != nil {
				return fmt.Errorf("upload tar: mkdir %s: %w", hdr.Name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("upload tar: mkdir parent %s: %w", hdr.Name, err)
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return fmt.Errorf("upload tar: create %s: %w", hdr.Name, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return fmt.Errorf("upload tar: write %s: %w", hdr.Name, err)
		}
		f.Close()
	}
	return nil
}

// DownloadTar writes a tar archive of the sandbox filesystem to w.
// The archive paths are relative to the sandbox root.
func (s *Sandbox) DownloadTar(w io.Writer) error {
	root := s.fs.Root()
	tw := tar.NewWriter(w)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		hdr, hdrErr := tar.FileInfoHeader(info, "")
		if hdrErr != nil {
			return fmt.Errorf("download tar: header for %s: %w", rel, hdrErr)
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("download tar: write header %s: %w", rel, err)
		}
		if !info.IsDir() {
			f, openErr := os.Open(path)
			if openErr != nil {
				return fmt.Errorf("download tar: open %s: %w", rel, openErr)
			}
			defer f.Close()
			if _, copyErr := io.Copy(tw, f); copyErr != nil {
				return fmt.Errorf("download tar: copy %s: %w", rel, copyErr)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// resolvePath converts a user-supplied path to the real on-disk path.
// Relative paths are resolved against the current working directory.
// Virtual absolute paths (e.g. /home/user/foo) are translated to their real
// counterparts under the sandbox temp directory.
func (s *Sandbox) resolvePath(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.state.Cwd, path)
	}
	return pathmap.VirtualToReal(s.tempDir, path)
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
