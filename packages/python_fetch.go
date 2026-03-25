package packages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// DefaultPythonWASMURL is the download URL for the WASI-compiled CPython 3.12
// binary produced by the WebAssembly Language Runtimes project.
// Pass this to FetchPythonWASM as the url parameter.
const DefaultPythonWASMURL = "https://github.com/vmware-labs/webassembly-language-runtimes/releases/download/python%2F3.12.0%2B20231211-040d5a6/python-3.12.0.wasm"

// DefaultPythonWASMCacheDir returns the default on-host cache directory used
// by FetchPythonWASM (~/.cache/agentic-bash/python-wasm).
func DefaultPythonWASMCacheDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "agentic-bash", "python-wasm")
	}
	return filepath.Join(os.TempDir(), "agentic-bash-python-wasm")
}

// FetchPythonWASM downloads a python.wasm WASI binary from url, caches it at
// filepath.Join(cacheDir, "python.wasm"), and returns the raw bytes.
// Subsequent calls with the same cacheDir return the cached copy without
// re-downloading.
//
// Typical usage:
//
//	wasmBytes, err := packages.FetchPythonWASM(ctx,
//	    packages.DefaultPythonWASMURL,
//	    packages.DefaultPythonWASMCacheDir(),
//	)
//	sb, err := sandbox.New(sandbox.Options{
//	    PythonRuntime:  sandbox.PythonRuntimeWASM,
//	    PythonWASMBytes: wasmBytes,
//	})
func FetchPythonWASM(ctx context.Context, url, cacheDir string) ([]byte, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("python wasm: create cache dir: %w", err)
	}

	cachePath := filepath.Join(cacheDir, "python.wasm")
	if data, err := os.ReadFile(cachePath); err == nil {
		return data, nil // cache hit — skip download
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("python wasm: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("python wasm: download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("python wasm: download: HTTP %d from %s", resp.StatusCode, url)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("python wasm: read response body: %w", err)
	}

	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		// Non-fatal: return the bytes even if caching failed.
		return data, fmt.Errorf("python wasm: write cache: %w", err)
	}
	return data, nil
}
