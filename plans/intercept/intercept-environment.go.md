# executor/intercept/environment.go

Env, printenv, which — all read from the HandlerContext environment.

```go
package intercept

import (
	"context"
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

// NewEnvInterceptors returns environment-related command interceptors.
func NewEnvInterceptors(cfg Config) []Interceptor {
	return []Interceptor{
		&envInterceptor{},
		&printenvInterceptor{},
		&whichInterceptor{},
	}
}

// ─── env ─────────────────────────────────────────────────────────────────────

type envInterceptor struct{}

func (e *envInterceptor) Name() string { return "env" }
func (e *envInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	// env with no args (or only flags): print all vars.
	// env VAR=val cmd ...: set vars and run cmd — fall through for that case.
	if len(args) > 1 {
		// Check if it's a command invocation (first non-flag non-assignment arg).
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
				// It's `env cmd args` — we don't handle execution redirection here.
				// Return exit 127 to signal "not handled" isn't valid; instead we
				// forward this to next via a sentinel. For now, print error.
				fmt.Fprintf(hc.Stderr, "env: command execution not supported in shim\n")
				return interp.NewExitStatus(1)
			}
		}
	}

	// Print all environment variables from the runner's env.
	hc.Env.Each(func(name string, vr interface{ String() string }) bool {
		fmt.Fprintf(hc.Stdout, "%s=%s\n", name, vr.String())
		return true
	})
	return nil
}

// ─── printenv ────────────────────────────────────────────────────────────────

type printenvInterceptor struct{}

func (p *printenvInterceptor) Name() string { return "printenv" }
func (p *printenvInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	if len(args) == 1 {
		// printenv with no args = same as env
		hc.Env.Each(func(name string, vr interface{ String() string }) bool {
			fmt.Fprintf(hc.Stdout, "%s=%s\n", name, vr.String())
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

// ─── which ───────────────────────────────────────────────────────────────────

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
			continue // skip flags like -a
		}
		// Use interp.LookPath which respects the runner's $PATH env.
		path, err := interp.LookPath(hc.Env, name)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "which: no %s in PATH\n", name)
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
```

## Note on env.Each

`hc.Env` is `expand.Environ`. The interface has `Each(func(name string, vr expand.Variable) bool)`.
The actual signature is:
```go
hc.Env.Each(func(name string, vr expand.Variable) bool {
    fmt.Fprintf(hc.Stdout, "%s=%s\n", name, vr.Str)
    return true
})
```
Adjust the `env` and `printenv` implementations to match the real `expand.Variable` type (not the interface shown above — that's pseudocode for illustration).
