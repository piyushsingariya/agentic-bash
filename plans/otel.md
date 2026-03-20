# Plan: OpenTelemetry Tracing & Metrics (`otel` build tag)

## Goal

Add optional OpenTelemetry (OTel) instrumentation to the sandbox so that
operators can observe:
- **Traces**: one span per `Run()` / `RunStream()` call, with child spans for
  external commands spawned inside the shell.
- **Metrics**: counters and histograms for run count, duration, exit codes,
  CPU/memory usage, and output bytes.

All OTel code is gated behind a `//go:build otel` build tag so the default
binary has zero OTel overhead and no extra dependencies.

---

## Dependencies (otel build only)

```
go get go.opentelemetry.io/otel@latest
go get go.opentelemetry.io/otel/trace@latest
go get go.opentelemetry.io/otel/metric@latest
go get go.opentelemetry.io/otel/sdk/trace@latest
go get go.opentelemetry.io/otel/sdk/metric@latest
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@latest
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc@latest
```

These are added as `// indirect` under a build constraint in `go.mod` so they
don't appear in non-otel builds.

---

## Architecture

```
sandbox.Run()  ──► otelSandbox.Run()  ──► underlying Sandbox.Run()
                      │                         │
                      ▼                         ▼
                 start span              ExecHandler fires per external cmd
                 record attrs            ── otelExecHandler wraps it
                 end span                    start child span
                                             end child span
```

Two patterns:
1. **Wrapper type** `otelSandbox` — wraps `*sandbox.Sandbox`; intercepts
   `Run()` / `RunStream()` to manage the parent span.
2. **Instrumented ExecHandler** — wraps `NewIsolatedExecHandler` to add a child
   span per external command.

---

## Files to create

| Path | Tag | Description |
|---|---|---|
| `telemetry/telemetry.go` | `otel` | OTel setup, provider init/shutdown |
| `telemetry/telemetry_noop.go` | `!otel` | No-op stubs |
| `telemetry/sandbox.go` | `otel` | `WrapSandbox(s *sandbox.Sandbox, tracer, meter) SandboxRunner` |
| `telemetry/exechandler.go` | `otel` | `WrapExecHandler(h interp.ExecHandlerFunc, tracer) interp.ExecHandlerFunc` |

No changes to the `sandbox` package itself — OTel is opt-in at the call site.

---

## `telemetry/telemetry.go` (otel tag)

```go
//go:build otel

package telemetry

import (
    "context"
    "os"
    "time"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const defaultEndpoint = "localhost:4317"

// Config controls OTel provider setup.
type Config struct {
    // OTLPEndpoint is the gRPC endpoint for the OTel collector.
    // Defaults to OTEL_EXPORTER_OTLP_ENDPOINT env var, then localhost:4317.
    OTLPEndpoint string

    // ServiceName is set as the resource service.name attribute.
    ServiceName string

    // ShutdownTimeout is the max time to flush spans/metrics on Shutdown.
    ShutdownTimeout time.Duration
}

// Provider holds the initialised OTel SDK providers.
type Provider struct {
    tp *sdktrace.TracerProvider
    mp *sdkmetric.MeterProvider
}

// Init creates and registers global trace and metric providers.
// Call Shutdown() when done to flush pending data.
func Init(ctx context.Context, cfg Config) (*Provider, error) {
    if cfg.OTLPEndpoint == "" {
        cfg.OTLPEndpoint = envOr("OTEL_EXPORTER_OTLP_ENDPOINT", defaultEndpoint)
    }
    if cfg.ServiceName == "" {
        cfg.ServiceName = "agentic-bash"
    }
    if cfg.ShutdownTimeout == 0 {
        cfg.ShutdownTimeout = 5 * time.Second
    }

    traceExp, err := otlptracegrpc.New(ctx,
        otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
        otlptracegrpc.WithInsecure())
    if err != nil {
        return nil, fmt.Errorf("otlp trace exporter: %w", err)
    }

    metricExp, err := otlpmetricgrpc.New(ctx,
        otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint),
        otlpmetricgrpc.WithInsecure())
    if err != nil {
        return nil, fmt.Errorf("otlp metric exporter: %w", err)
    }

    res := resource.NewWithAttributes(semconv.SchemaURL,
        semconv.ServiceName(cfg.ServiceName))

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(traceExp),
        sdktrace.WithResource(res))
    otel.SetTracerProvider(tp)

    mp := sdkmetric.NewMeterProvider(
        sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
        sdkmetric.WithResource(res))
    otel.SetMeterProvider(mp)

    return &Provider{tp: tp, mp: mp}, nil
}

func (p *Provider) Shutdown(ctx context.Context) {
    _ = p.tp.Shutdown(ctx)
    _ = p.mp.Shutdown(ctx)
}

func envOr(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

---

## `telemetry/sandbox.go` (otel tag)

```go
//go:build otel

package telemetry

import (
    "context"
    "io"
    "strconv"

    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/metric"
    "go.opentelemetry.io/otel/trace"

    "github.com/piyushsingariya/agentic-bash/sandbox"
)

const instrScope = "agentic-bash/sandbox"

// OtelSandbox wraps a sandbox.Sandbox with OTel tracing and metrics.
type OtelSandbox struct {
    s      *sandbox.Sandbox
    tracer trace.Tracer
    // metrics
    runCounter    metric.Int64Counter
    runDuration   metric.Float64Histogram
    outputBytes   metric.Int64Histogram
    cpuUsec       metric.Int64Histogram
    memPeakMB     metric.Int64Histogram
}

// Wrap returns an OtelSandbox that adds a trace span and updates metrics
// for every Run() / RunStream() call.
func Wrap(s *sandbox.Sandbox) *OtelSandbox {
    tracer := otel.Tracer(instrScope)
    meter  := otel.Meter(instrScope)

    runCounter, _  := meter.Int64Counter("sandbox.run.count")
    runDuration, _ := meter.Float64Histogram("sandbox.run.duration_ms")
    outputBytes, _ := meter.Int64Histogram("sandbox.run.output_bytes")
    cpuUsec, _     := meter.Int64Histogram("sandbox.run.cpu_usec")
    memPeakMB, _   := meter.Int64Histogram("sandbox.run.mem_peak_mb")

    return &OtelSandbox{s: s, tracer: tracer,
        runCounter: runCounter, runDuration: runDuration,
        outputBytes: outputBytes, cpuUsec: cpuUsec, memPeakMB: memPeakMB}
}

func (o *OtelSandbox) Run(cmd string) sandbox.ExecutionResult {
    ctx, span := o.tracer.Start(context.Background(), "sandbox.Run",
        trace.WithAttributes(attribute.String("cmd.preview", preview(cmd))))
    defer span.End()

    r := o.s.Run(cmd)
    o.recordResult(ctx, span, r)
    return r
}

func (o *OtelSandbox) RunStream(ctx context.Context, cmd string, stdout, stderr io.Writer) (int, error) {
    ctx, span := o.tracer.Start(ctx, "sandbox.RunStream",
        trace.WithAttributes(attribute.String("cmd.preview", preview(cmd))))
    defer span.End()

    exitCode, err := o.s.RunStream(ctx, cmd, stdout, stderr)
    span.SetAttributes(attribute.Int("exit_code", exitCode))
    if err != nil {
        span.RecordError(err)
    }
    return exitCode, err
}

func (o *OtelSandbox) recordResult(ctx context.Context, span trace.Span, r sandbox.ExecutionResult) {
    attrs := attribute.NewSet(
        attribute.Int("exit_code", r.ExitCode),
        attribute.Bool("error", r.Error != nil),
    )
    span.SetAttributes(attrs.ToSlice()...)
    if r.Error != nil {
        span.RecordError(r.Error)
    }

    durationMs := float64(r.Duration.Milliseconds())
    outBytes   := int64(len(r.Stdout) + len(r.Stderr))

    o.runCounter.Add(ctx, 1, metric.WithAttributeSet(attrs))
    o.runDuration.Record(ctx, durationMs, metric.WithAttributeSet(attrs))
    o.outputBytes.Record(ctx, outBytes, metric.WithAttributeSet(attrs))
    if r.CPUTime > 0 {
        o.cpuUsec.Record(ctx, r.CPUTime.Microseconds(), metric.WithAttributeSet(attrs))
    }
    if r.MemoryPeakMB > 0 {
        o.memPeakMB.Record(ctx, int64(r.MemoryPeakMB), metric.WithAttributeSet(attrs))
    }
}

// preview truncates cmd to 80 chars for use as a span attribute.
func preview(cmd string) string {
    if len(cmd) > 80 {
        return cmd[:80] + "…"
    }
    return cmd
}
```

---

## `telemetry/exechandler.go` (otel tag)

```go
//go:build otel

package telemetry

import (
    "context"

    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
    "mvdan.cc/sh/v3/interp"
)

// WrapExecHandler returns an ExecHandlerFunc that creates a child OTel span
// for each external command spawned by the shell interpreter.
// Must be called after the parent span has been started (i.e. inside Run/RunStream).
func WrapExecHandler(h interp.ExecHandlerFunc, tracer trace.Tracer) interp.ExecHandlerFunc {
    return func(ctx context.Context, args []string) error {
        spanName := "exec:" + args[0]
        ctx, span := tracer.Start(ctx, spanName,
            trace.WithAttributes(
                attribute.StringSlice("args", args),
            ))
        defer span.End()

        err := h(ctx, args)
        if err != nil {
            if status, ok := interp.IsExitStatus(err); ok {
                span.SetAttributes(attribute.Int("exit_code", int(status)))
            } else {
                span.RecordError(err)
            }
        }
        return err
    }
}
```

To wire this into the sandbox, `OtelSandbox.Wrap()` can inject the wrapped
exec handler via `sandbox.Options.OnCommand` or a future `ExecHandlerMiddleware`
option. For MVP, document this as manual wiring:

```go
// In user code (otel build):
tel, _ := telemetry.Init(ctx, telemetry.Config{ServiceName: "my-agent"})
defer tel.Shutdown(ctx)

sb, _ := sandbox.New(sandbox.Options{...})
otelSb := telemetry.Wrap(sb)
// Use otelSb.Run() / otelSb.RunStream() instead of sb directly.
```

---

## `telemetry/telemetry_noop.go` (!otel tag)

```go
//go:build !otel

package telemetry

import (
    "context"
    "github.com/piyushsingariya/agentic-bash/sandbox"
)

type Config struct{}
type Provider struct{}

func Init(_ context.Context, _ Config) (*Provider, error) { return &Provider{}, nil }
func (p *Provider) Shutdown(_ context.Context) {}

type OtelSandbox struct{ s *sandbox.Sandbox }
func Wrap(s *sandbox.Sandbox) *OtelSandbox { return &OtelSandbox{s: s} }
func (o *OtelSandbox) Run(cmd string) sandbox.ExecutionResult { return o.s.Run(cmd) }
func (o *OtelSandbox) RunStream(ctx context.Context, cmd string, w, e io.Writer) (int, error) {
    return o.s.RunStream(ctx, cmd, w, e)
}
```

---

## CLI integration (otel build)

When built with `-tags otel`, the CLI auto-initialises OTel if
`OTEL_EXPORTER_OTLP_ENDPOINT` is set:

```go
//go:build otel

func maybeInitOtel(ctx context.Context) func() {
    endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
    if endpoint == "" {
        return func() {}
    }
    provider, err := telemetry.Init(ctx, telemetry.Config{})
    if err != nil {
        fmt.Fprintf(os.Stderr, "otel init: %v\n", err)
        return func() {}
    }
    return func() { provider.Shutdown(ctx) }
}
```

Called at the top of `main()` (otel build only):
```go
defer maybeInitOtel(context.Background())()
```

---

## Standard metric names

| Metric | Type | Unit | Description |
|---|---|---|---|
| `sandbox.run.count` | Counter | 1 | Total `Run()` calls |
| `sandbox.run.duration_ms` | Histogram | ms | Wall-clock duration per run |
| `sandbox.run.output_bytes` | Histogram | By | Combined stdout+stderr bytes |
| `sandbox.run.cpu_usec` | Histogram | μs | CPU time (from cgroupv2) |
| `sandbox.run.mem_peak_mb` | Histogram | MiBy | Peak memory (from cgroupv2) |

Standard attributes on all metrics/spans:
- `exit_code` — integer
- `error` — boolean
- `isolation` — string (`namespace`, `landlock`, `noop`)

---

## Tests

Tests live in `telemetry/telemetry_test.go` with `//go:build otel`.

```go
//go:build otel

func TestOtelWrapRunRecordsSpan(t *testing.T) {
    // Use an in-memory exporter to verify spans are recorded.
    exp := tracetest.NewInMemoryExporter()
    tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
    otel.SetTracerProvider(tp)

    sb, _ := sandbox.New(sandbox.Options{})
    otelSb := telemetry.Wrap(sb)
    _ = otelSb.Run(`echo hello`)

    spans := exp.GetSpans()
    if len(spans) == 0 {
        t.Fatal("expected at least one span")
    }
    if spans[0].Name != "sandbox.Run" {
        t.Errorf("span name %q, want sandbox.Run", spans[0].Name)
    }
}
```

---

## Key design decisions

1. **Opt-in via build tag**: Keeps the default binary free of OTel deps (~5 MiB
   of SDK code). Users who want observability build with `-tags otel`.

2. **Wrapper pattern, not embedding**: The `sandbox` package remains unaware of
   OTel. This keeps the core dependency-free and lets users choose their own
   exporters/providers.

3. **No context threading through `Run()`**: `sandbox.Run()` does not accept a
   `context.Context`. `OtelSandbox.Run()` uses `context.Background()` as the
   root; `RunStream()` threads the provided context. This is the cleanest
   approach without modifying the sandbox API.

4. **Auto-init from env var**: Following OTel conventions, the presence of
   `OTEL_EXPORTER_OTLP_ENDPOINT` triggers automatic provider setup, with no
   code changes required.
