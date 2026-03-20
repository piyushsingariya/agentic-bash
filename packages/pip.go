package packages

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

// PipShim intercepts pip / pip3 / python -m pip commands and redirects the
// install target to the sandbox overlay so host site-packages are untouched.
type PipShim struct {
	cfg ShimConfig
}

// Matches reports whether args is a pip install/uninstall invocation.
//
// Recognised forms:
//
//	pip install/uninstall [...]
//	pip3 install/uninstall [...]
//	python -m pip install/uninstall [...]
//	python3 -m pip install/uninstall [...]
func (s *PipShim) Matches(args []string) bool {
	if len(args) < 2 {
		return false
	}
	base := filepath.Base(args[0])
	switch base {
	case "pip", "pip3":
		return args[1] == "install" || args[1] == "uninstall"
	case "python", "python3":
		// python -m pip install/uninstall ...
		return len(args) >= 4 && args[1] == "-m" && args[2] == "pip" &&
			(args[3] == "install" || args[3] == "uninstall")
	}
	return false
}

func (s *PipShim) handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	// Locate the subcommand index (install | uninstall).
	subIdx := -1
	for i, a := range args {
		if a == "install" || a == "uninstall" {
			subIdx = i
			break
		}
	}
	if subIdx < 0 {
		return s.passThrough(ctx, hc, args)
	}

	switch args[subIdx] {
	case "install":
		return s.install(ctx, hc, args[subIdx+1:])
	case "uninstall":
		return s.uninstall(ctx, hc, args[subIdx+1:])
	default:
		return s.passThrough(ctx, hc, args)
	}
}

func (s *PipShim) install(ctx context.Context, hc interp.HandlerContext, pkgArgs []string) error {
	target := OverlayPythonPath(s.cfg.OverlayRoot)
	if err := ensureDir(target); err != nil {
		return fmt.Errorf("pip: create target dir: %w", err)
	}

	// Find pip3 first, then pip, via the shell's PATH.
	pipBin, err := lookBin(hc, "pip3")
	if err != nil {
		if pipBin, err = lookBin(hc, "pip"); err != nil {
			return interp.NewExitStatus(127)
		}
	}

	// Strip any caller-supplied --target flags; we enforce our own.
	stripped := stripFlag(pkgArgs, "--target")
	stripped = stripFlag(stripped, "-t")

	if len(stripped) == 0 {
		return nil // nothing to install after stripping
	}

	cmdArgs := append([]string{pipBin, "install", "--target=" + target, "--quiet"}, stripped...)
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdout = hc.Stdout
	cmd.Stderr = hc.Stderr
	cmd.Dir = hc.Dir
	cmd.Env = envToSlice(hc.Env)
	if err := cmd.Run(); err != nil {
		return toExitErr(err)
	}

	// Record non-flag arguments in the manifest.
	if s.cfg.Manifest != nil {
		for _, a := range stripped {
			if !strings.HasPrefix(a, "-") {
				name, version := splitPkgSpec(a)
				s.cfg.Manifest.Record(PackageInfo{Name: name, Version: version, Manager: "pip"})
			}
		}
	}
	return nil
}

func (s *PipShim) uninstall(_ context.Context, _ interp.HandlerContext, pkgArgs []string) error {
	target := OverlayPythonPath(s.cfg.OverlayRoot)

	for _, a := range pkgArgs {
		if strings.HasPrefix(a, "-") {
			continue
		}
		name, _ := splitPkgSpec(a)

		// pip installs as <name>/ and <name>-<ver>.dist-info/ (and .data/).
		// Walk the target dir looking for entries that match the package name.
		entries, _ := os.ReadDir(target)
		lower := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
		for _, e := range entries {
			base := strings.ToLower(strings.ReplaceAll(e.Name(), "-", "_"))
			if base == lower || strings.HasPrefix(base, lower+"-") || strings.HasPrefix(base, lower+".") {
				_ = os.RemoveAll(filepath.Join(target, e.Name()))
			}
		}

		if s.cfg.Manifest != nil {
			s.cfg.Manifest.Remove(name, "pip")
		}
	}
	return nil
}

// passThrough runs the original pip command unchanged via os/exec, forwarding
// the handler context's stdio.  Used for pip subcommands we don't intercept.
func (s *PipShim) passThrough(ctx context.Context, hc interp.HandlerContext, args []string) error {
	bin, err := lookBin(hc, args[0])
	if err != nil {
		return interp.NewExitStatus(127)
	}
	cmd := exec.CommandContext(ctx, bin, args[1:]...)
	cmd.Stdout = hc.Stdout
	cmd.Stderr = hc.Stderr
	cmd.Stdin = hc.Stdin
	cmd.Dir = hc.Dir
	cmd.Env = envToSlice(hc.Env)
	return toExitErr(cmd.Run())
}

// splitPkgSpec splits a PEP 440 specifier like "requests==2.28" into name and
// version.  Returns the full spec as name and empty version when no pin is
// present.
func splitPkgSpec(spec string) (name, version string) {
	for _, sep := range []string{"==", ">=", "<=", "~=", "!="} {
		if i := strings.Index(spec, sep); i > 0 {
			return spec[:i], spec[i+len(sep):]
		}
	}
	return spec, ""
}
