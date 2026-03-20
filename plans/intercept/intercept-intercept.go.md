# executor/intercept/intercept.go

Core types: Config, Interceptor interface, Dispatcher middleware, AuditMiddleware.

```go
package intercept

import (
	"context"
	"fmt"
	"io"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// ExecHandlerFunc mirrors executor.ExecHandlerFunc to avoid circular import.
type ExecHandlerFunc = interp.ExecHandlerFunc

// Config holds the synthetic identity and sandbox root used by all interceptors.
type Config struct {
	UserName    string // e.g. "user"
	Hostname    string // e.g. "sandbox"
	UID         int    // e.g. 1000
	GID         int    // e.g. 1000
	SandboxRoot string // real on-disk temp dir root
}

// Interceptor handles one external command by name.
type Interceptor interface {
	// Name returns the command name this interceptor claims (e.g. "ls").
	Name() string
	// Handle executes the command. hc is the mvdan HandlerContext from ctx.
	Handle(ctx context.Context, args []string) error
}

// NewDispatcher returns an ExecHandlers-compatible middleware that routes
// matched command names to registered Interceptors and forwards the rest to next.
//
// Usage:
//   interp.ExecHandlers(
//       NewDispatcher(cfg,
//           NewSysInfoInterceptors(cfg)...,
//           NewFilesystemInterceptors(cfg)...,
//           NewEnvInterceptors(cfg)...,
//       ),
//       ...
//   )
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
// w may be nil (audit disabled).
func NewAuditMiddleware(w io.Writer) func(next ExecHandlerFunc) ExecHandlerFunc {
	return func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if w != nil {
				fmt.Fprintf(w, "[%s] exec: %v\n", time.Now().Format(time.RFC3339), args)
			}
			return next(ctx, args)
		}
	}
}
```
