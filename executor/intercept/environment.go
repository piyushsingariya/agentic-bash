package intercept

import (
	"context"
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

// NewEnvInterceptors returns environment-related command interceptors.
func NewEnvInterceptors(_ Config) []Interceptor {
	return []Interceptor{
		&envInterceptor{},
		&printenvInterceptor{},
		&whichInterceptor{},
	}
}

// ─── env ──────────────────────────────────────────────────────────────────────

type envInterceptor struct{}

func (e *envInterceptor) Name() string { return "env" }
func (e *envInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	// If any non-flag non-assignment arg is present, that's a command to run —
	// we don't support exec-via-env in the shim.
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			fmt.Fprintf(hc.Stderr, "env: command execution not supported in shim: %s\n", a)
			return interp.NewExitStatus(1)
		}
	}

	hc.Env.Each(func(name string, vr expand.Variable) bool {
		if vr.IsSet() {
			fmt.Fprintf(hc.Stdout, "%s=%s\n", name, vr.Str)
		}
		return true
	})
	return nil
}

// ─── printenv ─────────────────────────────────────────────────────────────────

type printenvInterceptor struct{}

func (p *printenvInterceptor) Name() string { return "printenv" }
func (p *printenvInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	if len(args) == 1 {
		hc.Env.Each(func(name string, vr expand.Variable) bool {
			if vr.IsSet() {
				fmt.Fprintf(hc.Stdout, "%s=%s\n", name, vr.Str)
			}
			return true
		})
		return nil
	}

	exitCode := uint8(0)
	for _, name := range args[1:] {
		v := hc.Env.Get(name)
		if !v.IsSet() {
			exitCode = 1
			continue
		}
		fmt.Fprintln(hc.Stdout, v.Str)
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── which ────────────────────────────────────────────────────────────────────

type whichInterceptor struct{}

func (w *whichInterceptor) Name() string { return "which" }
func (w *whichInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) < 2 {
		fmt.Fprintln(hc.Stderr, "which: missing argument")
		return interp.NewExitStatus(1)
	}

	exitCode := uint8(0)
	for _, name := range args[1:] {
		if strings.HasPrefix(name, "-") {
			continue
		}
		path, err := interp.LookPath(hc.Env, name)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "which: no %s in ($PATH)\n", name)
			exitCode = 1
			continue
		}
		fmt.Fprintln(hc.Stdout, path)
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}
