package otel

import (
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// newLoggerProvider builds a logger provider with a batch log processor over
// the given exporter and resource.
func newLoggerProvider(res *resource.Resource, exporter sdklog.Exporter) *sdklog.LoggerProvider {
	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)
}
