package packages

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

// ShimConfig carries the dependencies required by all package-manager shims.
type ShimConfig struct {
	// OverlayRoot is the writable sandbox filesystem root (sandbox.tempDir).
	// pip packages land in OverlayRoot/lib/python3/site-packages;
	// apt packages are extracted into OverlayRoot as if it were /.
	OverlayRoot string

	// Manifest records every package installed in this sandbox session.
	Manifest *Manifest

	// CacheDir is the on-host shared cache (~/.cache/agentic-bash/packages).
	// Zero value disables caching (packages are re-downloaded every time).
	CacheDir string
}

// NewShimHandler returns an interp.ExecHandlerFunc that intercepts pip and
// apt-get commands and redirects them into the sandbox overlay.  Unrecognised
// commands are forwarded to next (the isolation exec handler).
func NewShimHandler(cfg ShimConfig, next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	pip := &PipShim{cfg: cfg}
	apt := &AptShim{cfg: cfg}
	return func(ctx context.Context, args []string) error {
		switch {
		case pip.Matches(args):
			return pip.handle(ctx, args)
		case apt.Matches(args):
			return apt.handle(ctx, args)
		default:
			return next(ctx, args)
		}
	}
}

// OverlayPythonPath returns the Python site-packages directory inside the
// overlay.  This path is injected as PYTHONPATH at sandbox creation so that
// packages installed via pip are immediately importable.
func OverlayPythonPath(overlayRoot string) string {
	return filepath.Join(overlayRoot, "lib", "python3", "site-packages")
}

// OverlayBinPath returns the primary binary directory inside the overlay.
// This path is prepended to PATH at sandbox creation so that binaries
// installed via apt are found before host binaries.
func OverlayBinPath(overlayRoot string) string {
	return filepath.Join(overlayRoot, "usr", "local", "bin")
}

// ensureDir creates dir and all parents; a no-op if the dir already exists.
func ensureDir(dir string) error { return os.MkdirAll(dir, 0o755) }

// envToSlice converts an expand.Environ into the KEY=VALUE slice format
// expected by exec.Cmd.Env.  Only exported string variables are included.
func envToSlice(env expand.Environ) []string {
	var out []string
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String {
			out = append(out, name+"="+vr.Str)
		}
		return true
	})
	return out
}

// toExitErr converts an os/exec error into the interp exit-status type that
// mvdan.cc/sh understands.  Non-exit errors (e.g. binary not found) are
// returned verbatim.
func toExitErr(err error) error {
	if err == nil {
		return nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		if code < 0 || code > 255 {
			code = 1
		}
		return interp.NewExitStatus(uint8(code))
	}
	return err
}

// lookBin resolves a binary name via the shell handler context's PATH,
// matching the behaviour of the shell's own PATH resolution.
// Returns the absolute path or ("", err) if not found.
func lookBin(hc interp.HandlerContext, name string) (string, error) {
	return interp.LookPathDir(hc.Dir, hc.Env, name)
}

// stripFlag removes all occurrences of a flag and its value from args.
// Handles both "--flag value" and "--flag=value" forms.
func stripFlag(args []string, flag string) []string {
	out := args[:0]
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		if a == flag {
			skip = true
			continue
		}
		if strings.HasPrefix(a, flag+"=") {
			continue
		}
		out = append(out, a)
	}
	return out
}
