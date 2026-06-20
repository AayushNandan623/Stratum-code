// Package telemetry initializes OpenTelemetry providers for the process.
//
// Phase 0 installs a no-op tracer provider only: tracing is wired through the
// standard otel APIs so call sites can be added now, but no spans are exported
// and there is no OTLP exporter. Real tracing (OTLP exporter, sampling,
// resource attributes) is added in Phase 4+.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

// InitTracer installs a no-op tracer provider as the global provider and
// returns a shutdown function. The shutdown function is always a no-op in
// Phase 0 but is returned so callers can wire it into graceful shutdown now.
func InitTracer(ctx context.Context) (func(context.Context) error, error) {
	_ = ctx
	tp := noop.NewTracerProvider()
	otel.SetTracerProvider(tp)
	return func(context.Context) error { return nil }, nil
}
