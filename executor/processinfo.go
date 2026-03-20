package executor

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
	"mvdan.cc/sh/v3/interp"
)

// ProcessInfoConfig holds the synthetic identity values returned by process
// info commands (whoami, hostname, id, uname) and the sandbox root used to
// translate virtual paths for the ls shim.
type ProcessInfoConfig struct {
	UserName    string
	Hostname    string
	UID         int
	GID         int
	SandboxRoot string // real on-disk root; used by the ls shim
}

// NewProcessInfoHandler returns an ExecHandlerFunc that intercepts common
// process-identity and filesystem-listing commands and returns virtual output,
// forwarding everything else to next.
//
// Intercepted commands: pwd, whoami, hostname, id, uname, ls.
func NewProcessInfoHandler(cfg ProcessInfoConfig, next ExecHandlerFunc) ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 {
			return next(ctx, args)
		}
		hc := interp.HandlerCtx(ctx)

		switch args[0] {
		case "pwd":
			// The native pwd binary would report the real tmpdir path via getcwd().
			// Instead, emit the virtual path from $PWD so the agent always sees
			// the clean virtual path (e.g. /home/user, /workspace).
			virtualPWD := hc.Env.Get("PWD").Str
			if virtualPWD == "" && cfg.SandboxRoot != "" {
				virtualPWD = pathmap.RealToVirtual(cfg.SandboxRoot, hc.Dir)
			}
			fmt.Fprintln(hc.Stdout, virtualPWD)
			return nil

		case "whoami":
			fmt.Fprintln(hc.Stdout, cfg.UserName)
			return nil

		case "hostname":
			fmt.Fprintln(hc.Stdout, cfg.Hostname)
			return nil

		case "id":
			fmt.Fprintf(hc.Stdout, "uid=%d(%s) gid=%d(%s) groups=%d(%s)\n",
				cfg.UID, cfg.UserName, cfg.GID, cfg.UserName, cfg.GID, cfg.UserName)
			return nil

		case "uname":
			return handleUname(hc, args[1:], cfg.Hostname)

		case "ls":
			if cfg.SandboxRoot != "" {
				return handleLs(hc, args[1:], cfg.SandboxRoot)
			}
		}

		return next(ctx, args)
	}
}

func handleUname(hc interp.HandlerContext, args []string, hostname string) error {
	if len(args) == 0 {
		fmt.Fprintln(hc.Stdout, "Linux")
		return nil
	}
	// Combine all flags into one string for easy checking.
	combined := strings.Join(args, "")
	if strings.Contains(combined, "a") {
		fmt.Fprintf(hc.Stdout, "Linux %s 6.1.0-agentic #1 SMP x86_64 GNU/Linux\n", hostname)
		return nil
	}
	for _, flag := range args {
		switch flag {
		case "-n", "--nodename":
			fmt.Fprintln(hc.Stdout, hostname)
		case "-s", "--kernel-name":
			fmt.Fprintln(hc.Stdout, "Linux")
		case "-r", "--kernel-release":
			fmt.Fprintln(hc.Stdout, "6.1.0-agentic")
		case "-m", "--machine":
			fmt.Fprintln(hc.Stdout, "x86_64")
		case "-o", "--operating-system":
			fmt.Fprintln(hc.Stdout, "GNU/Linux")
		}
	}
	return nil
}

func handleLs(hc interp.HandlerContext, args []string, sandboxRoot string) error {
	// Separate flags from path arguments.
	var paths []string
	showAll := false
	longFmt := false

	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if strings.Contains(a, "a") || strings.Contains(a, "A") {
				showAll = true
			}
			if strings.Contains(a, "l") {
				longFmt = true
			}
		} else {
			paths = append(paths, a)
		}
	}

	// Default to current (real) directory when no path given.
	if len(paths) == 0 {
		paths = []string{hc.Dir}
	}

	for _, p := range paths {
		// Translate virtual → real (idempotent if already real).
		realP := pathmap.VirtualToReal(sandboxRoot, p)
		// If given hc.Dir directly it is already real; VirtualToReal is safe.

		entries, err := os.ReadDir(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "ls: cannot access '%s': No such file or directory\n", p)
			continue
		}

		for _, e := range entries {
			name := e.Name()
			if !showAll && strings.HasPrefix(name, ".") {
				continue
			}
			if longFmt {
				info, infoErr := e.Info()
				if infoErr != nil {
					continue
				}
				fmt.Fprintf(hc.Stdout, "%s %8d %s\n", info.Mode(), info.Size(), name)
			} else {
				fmt.Fprintln(hc.Stdout, name)
			}
		}
	}
	return nil
}
