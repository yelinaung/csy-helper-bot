package otel

import (
	"fmt"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewHTTPTransport wraps base (or http.DefaultTransport when nil) with
// otelhttp, producing standard HTTP client spans and metrics. The span name
// is formatted as "<METHOD> <HOST>" (never the full URL) to avoid leaking
// credentials that some endpoints put in the path/query.
//
// NOTE: otelhttp resolves the tracer lazily per request, but it binds its
// client metric instruments EAGERLY at construction time from the global meter
// provider (newConfig → createMeasures). NewHTTPTransport must therefore be
// called AFTER Setup has installed the SDK meter provider, otherwise the
// metrics bind to the noop meter and are never exported. Call WrapClient from
// the bot's startup (after Setup), not from a package init().
func NewHTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(
		base,
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			// otelhttp passes an empty operation string for client spans, so
			// use the HTTP method explicitly to get names like "GET finnhub.io".
			method := operation
			if r != nil && r.Method != "" {
				method = r.Method
			}
			host := ""
			if r != nil && r.URL != nil {
				host = r.URL.Host
			}
			return fmt.Sprintf("%s %s", method, host)
		}),
	)
}

// WrapClient wraps c's transport with otelhttp, guarding against
// double-wrapping. It must be called after Setup has installed the global meter
// provider so otelhttp's client metrics bind to the real (or noop when
// telemetry is disabled) provider rather than the default noop meter.
func WrapClient(c *http.Client) {
	if c == nil {
		return
	}
	if _, ok := c.Transport.(*otelhttp.Transport); ok {
		return // already wrapped; avoid duplicate nested spans/metrics
	}
	c.Transport = NewHTTPTransport(c.Transport)
}
