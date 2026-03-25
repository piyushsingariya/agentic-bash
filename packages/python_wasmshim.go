package packages

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	wzsys "github.com/tetratelabs/wazero/sys"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

// PythonWASMShim intercepts python3/python commands and runs them inside a
// wazero WASI runtime instead of spawning a real OS process.
//
// Security properties (inherited from the WASI specification):
//   - subprocess spawning is impossible: WASI defines no fork/exec API.
//     subprocess.run(), os.system(), and os.popen() all fail with ENOSYS.
//   - filesystem access is scoped to the sandbox overlay; the host filesystem
//     is not visible to the WASM module.
//   - works identically on macOS and Linux (wazero is pure Go, zero CGO).
//
// Limitations:
//   - Python C extensions (numpy, pandas native code, etc.) cannot be loaded.
//   - The subprocess module fails at call time, not import time.
//   - Startup is ~100-500ms per invocation (first call compiles; subsequent
//     calls reuse the compiled module).
//
// pip commands (python3 -m pip ...) are handled by PipShim upstream and never
// reach this shim.
type PythonWASMShim struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	overlay  string
	seq      atomic.Int64 // unique instance counter; avoids name collisions
}

// NewPythonWASMShim compiles wasmBytes (a WASI python.wasm binary) and returns
// a shim ready for use. Compilation happens once here; each Run() call
// instantiates a fresh module from the pre-compiled artifact. Call Close()
// when the parent sandbox closes to release the wazero runtime.
func NewPythonWASMShim(ctx context.Context, wasmBytes []byte, overlayRoot string) (*PythonWASMShim, error) {
	r := wazero.NewRuntime(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("python wasm: instantiate wasi: %w", err)
	}

	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("python wasm: compile module: %w", err)
	}

	return &PythonWASMShim{
		runtime:  r,
		compiled: compiled,
		overlay:  overlayRoot,
	}, nil
}

// Close releases the wazero runtime. Must be called when the parent sandbox closes.
func (s *PythonWASMShim) Close(ctx context.Context) error {
	return s.runtime.Close(ctx)
}

// Matches reports whether args is a python3/python invocation.
func (s *PythonWASMShim) Matches(args []string) bool {
	if len(args) < 1 {
		return false
	}
	base := filepath.Base(args[0])
	return base == "python3" || base == "python"
}

// Handle runs the python3/python command inside the WASI sandbox.
func (s *PythonWASMShim) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	// Mount the sandbox overlay as "/" so the WASM module sees the virtual
	// filesystem. Packages installed by PipShim land at
	// <overlay>/lib/python3/site-packages → visible as /lib/python3/site-packages.
	fsConfig := wazero.NewFSConfig().WithDirMount(s.overlay, "/")

	// Each instantiation needs a unique module name within the runtime.
	seq := s.seq.Add(1)
	cfg := wazero.NewModuleConfig().
		WithName(fmt.Sprintf("python3-%d", seq)).
		WithStdout(hc.Stdout).
		WithStderr(hc.Stderr).
		WithStdin(hc.Stdin).
		WithFSConfig(fsConfig).
		WithArgs(args...).
		WithSysWalltime().
		WithSysNanosleep()

	// Forward the shell environment into the WASM module.
	hc.Env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String {
			cfg = cfg.WithEnv(name, vr.Str)
		}
		return true
	})

	mod, err := s.runtime.InstantiateModule(ctx, s.compiled, cfg)
	if mod != nil {
		_ = mod.Close(ctx)
	}
	if err == nil {
		return nil
	}
	// WASI proc_exit(n) surfaces as *sys.ExitError; convert to interp exit status.
	if exitErr, ok := err.(*wzsys.ExitError); ok {
		code := exitErr.ExitCode()
		if code > 255 {
			code = 1
		}
		return interp.ExitStatus(code)
	}
	return err
}

// NewPythonWASMMiddleware returns an ExecHandlers-compatible middleware that
// intercepts python3/python commands and runs them via the WASM shim,
// forwarding everything else to the next handler.
func NewPythonWASMMiddleware(shim *PythonWASMShim) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if shim.Matches(args) {
				return shim.Handle(ctx, args)
			}
			return next(ctx, args)
		}
	}
}
