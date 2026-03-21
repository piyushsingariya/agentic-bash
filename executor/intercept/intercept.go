package intercept

import (
	"context"
	"fmt"
	"io"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// ExecHandlerFunc mirrors interp.ExecHandlerFunc.
type ExecHandlerFunc = interp.ExecHandlerFunc

// Config holds the synthetic identity and sandbox root used by all interceptors.
type Config struct {
	UserName    string // e.g. "user"
	Hostname    string // e.g. "sandbox"
	UID         int    // e.g. 1000
	GID         int    // e.g. 1000
	SandboxRoot string // real on-disk temp dir root for virtual↔real translation
}

// Interceptor handles one external command by name.
type Interceptor interface {
	// Name returns the command name this interceptor claims (e.g. "ls").
	Name() string
	// Handle executes the command. Use interp.HandlerCtx(ctx) to get stdout/stderr/env.
	Handle(ctx context.Context, args []string) error
}

// NewDispatcher returns an ExecHandlers-compatible middleware that routes matched
// command names to registered Interceptors and forwards everything else to next.
func NewDispatcher(interceptors ...Interceptor) func(next ExecHandlerFunc) ExecHandlerFunc {
	registry := make(map[string]Interceptor, len(interceptors))
	for _, iv := range interceptors {
		registry[iv.Name()] = iv
	}
	return func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			if iv, ok := registry[args[0]]; ok {
				return iv.Handle(ctx, args)
			}
			return next(ctx, args)
		}
	}
}

// NewAuditMiddleware returns an ExecHandlers-compatible middleware that logs
// every external command invocation to w before forwarding to next.
// w may be nil (no-op).
func NewAuditMiddleware(w io.Writer) func(next ExecHandlerFunc) ExecHandlerFunc {
	return func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if w != nil {
				fmt.Fprintf(w, "[%s] exec: %v\n", time.Now().UTC().Format("15:04:05.000"), args)
			}
			return next(ctx, args)
		}
	}
}
