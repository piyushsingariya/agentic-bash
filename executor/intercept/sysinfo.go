package intercept

import (
	"context"
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/interp"

	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
)

// NewSysInfoInterceptors returns all process-identity command interceptors.
func NewSysInfoInterceptors(cfg Config) []Interceptor {
	return []Interceptor{
		&pwdInterceptor{cfg: cfg},
		&whoamiInterceptor{cfg: cfg},
		&hostnameInterceptor{cfg: cfg},
		&idInterceptor{cfg: cfg},
		&unameInterceptor{cfg: cfg},
	}
}

// ─── pwd ──────────────────────────────────────────────────────────────────────

type pwdInterceptor struct{ cfg Config }

func (p *pwdInterceptor) Name() string { return "pwd" }
func (p *pwdInterceptor) Handle(ctx context.Context, _ []string) error {
	hc := interp.HandlerCtx(ctx)
	virtualPWD := hc.Env.Get("PWD").Str
	if virtualPWD == "" && p.cfg.SandboxRoot != "" {
		virtualPWD = pathmap.RealToVirtual(p.cfg.SandboxRoot, hc.Dir)
	}
	fmt.Fprintln(hc.Stdout, virtualPWD)
	return nil
}

// ─── whoami ───────────────────────────────────────────────────────────────────

type whoamiInterceptor struct{ cfg Config }

func (w *whoamiInterceptor) Name() string { return "whoami" }
func (w *whoamiInterceptor) Handle(ctx context.Context, _ []string) error {
	hc := interp.HandlerCtx(ctx)
	fmt.Fprintln(hc.Stdout, w.cfg.UserName)
	return nil
}

// ─── hostname ─────────────────────────────────────────────────────────────────

type hostnameInterceptor struct{ cfg Config }

func (h *hostnameInterceptor) Name() string { return "hostname" }
func (h *hostnameInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	for _, a := range args[1:] {
		switch a {
		case "-i", "--ip-address":
			fmt.Fprintln(hc.Stdout, "127.0.0.1")
			return nil
		case "-f", "--fqdn", "--long":
			fmt.Fprintln(hc.Stdout, h.cfg.Hostname)
			return nil
		case "-s", "--short":
			fmt.Fprintln(hc.Stdout, strings.SplitN(h.cfg.Hostname, ".", 2)[0])
			return nil
		}
	}
	fmt.Fprintln(hc.Stdout, h.cfg.Hostname)
	return nil
}

// ─── id ───────────────────────────────────────────────────────────────────────

type idInterceptor struct{ cfg Config }

func (i *idInterceptor) Name() string { return "id" }
func (i *idInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	cfg := i.cfg

	nameMode := false
	for _, a := range args[1:] {
		switch a {
		case "-n", "--name":
			nameMode = true
		case "-u", "--user":
			if nameMode {
				fmt.Fprintln(hc.Stdout, cfg.UserName)
			} else {
				fmt.Fprintln(hc.Stdout, cfg.UID)
			}
			return nil
		case "-g", "--group":
			if nameMode {
				fmt.Fprintln(hc.Stdout, cfg.UserName)
			} else {
				fmt.Fprintln(hc.Stdout, cfg.GID)
			}
			return nil
		case "-G", "--groups":
			fmt.Fprintln(hc.Stdout, cfg.GID)
			return nil
		}
	}
	// Default full format.
	fmt.Fprintf(hc.Stdout, "uid=%d(%s) gid=%d(%s) groups=%d(%s)\n",
		cfg.UID, cfg.UserName, cfg.GID, cfg.UserName, cfg.GID, cfg.UserName)
	return nil
}

// ─── uname ────────────────────────────────────────────────────────────────────

const (
	unameKernel   = "Linux"
	unameRelease  = "6.1.0-agentic"
	unameVersion  = "#1 SMP x86_64"
	unameMachine  = "x86_64"
	unameOS       = "GNU/Linux"
	unameProc     = "x86_64"
	unamePlatform = "x86_64"
)

type unameInterceptor struct{ cfg Config }

func (u *unameInterceptor) Name() string { return "uname" }
func (u *unameInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	if len(args) == 1 {
		fmt.Fprintln(hc.Stdout, unameKernel)
		return nil
	}

	combined := strings.Join(args[1:], "")
	if strings.ContainsRune(combined, 'a') {
		fmt.Fprintf(hc.Stdout, "%s %s %s %s %s\n",
			unameKernel, u.cfg.Hostname, unameRelease, unameVersion, unameMachine)
		return nil
	}

	var parts []string
	for _, flag := range args[1:] {
		switch flag {
		case "-s", "--kernel-name":
			parts = append(parts, unameKernel)
		case "-n", "--nodename":
			parts = append(parts, u.cfg.Hostname)
		case "-r", "--kernel-release":
			parts = append(parts, unameRelease)
		case "-v", "--kernel-version":
			parts = append(parts, unameVersion)
		case "-m", "--machine":
			parts = append(parts, unameMachine)
		case "-p", "--processor":
			parts = append(parts, unameProc)
		case "-i", "--hardware-platform":
			parts = append(parts, unamePlatform)
		case "-o", "--operating-system":
			parts = append(parts, unameOS)
		}
	}
	if len(parts) > 0 {
		fmt.Fprintln(hc.Stdout, strings.Join(parts, " "))
	}
	return nil
}
