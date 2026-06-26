package otel

import (
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// newTracerProvider builds a tracer provider with a batch span processor over
// the (already sanitizing) exporter and the given resource.
func newTracerProvider(res *resource.Resource, exporter sdktrace.SpanExporter) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
	)
}
