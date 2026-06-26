package otel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
)

// sanitizingExporter wraps a trace.SpanExporter and strips credentials from
// any URL-bearing attribute (url.full or http.url) before export. It guards
// against Finnhub query tokens (token=<API_KEY>) and Telegram bot-token path
// segments (bot<TOKEN>) leaking into trace backends. It is always applied to
// the trace exporter in Setup when traces are enabled.
type sanitizingExporter struct {
	base trace.SpanExporter
}

// newSanitizingExporter wraps base so exported spans have credentials removed
// from url.full/http.url attributes before reaching the collector.
func newSanitizingExporter(base trace.SpanExporter) trace.SpanExporter {
	if base == nil {
		return nil
	}
	return &sanitizingExporter{base: base}
}

// ExportSpans redacts URL credentials from each span's attributes, then
// forwards to the wrapped exporter.
func (e *sanitizingExporter) ExportSpans(ctx context.Context, spans []trace.ReadOnlySpan) error {
	if e == nil || e.base == nil || len(spans) == 0 {
		return nil
	}
	redacted := make([]trace.ReadOnlySpan, 0, len(spans))
	for _, s := range spans {
		redacted = append(redacted, sanitizeReadOnlySpan{ReadOnlySpan: s})
	}
	return e.base.ExportSpans(ctx, redacted)
}

// Shutdown forwards to the wrapped exporter.
func (e *sanitizingExporter) Shutdown(ctx context.Context) error {
	if e == nil || e.base == nil {
		return nil
	}
	return e.base.Shutdown(ctx)
}

// sanitizeReadOnlySpan embeds a ReadOnlySpan and overrides the attribute-
// bearing accessors (Attributes, Events, Links) so URL credentials are
// redacted at export time. All other interface methods are delegated to the
// wrapped span via the embedded interface.
type sanitizeReadOnlySpan struct {
	trace.ReadOnlySpan
}

// Attributes returns the span attributes with credentials stripped from any
// URL-bearing attribute key.
func (s sanitizeReadOnlySpan) Attributes() []attribute.KeyValue {
	return redactURLAttrs(s.ReadOnlySpan.Attributes())
}

// Events returns the span events with credentials stripped from any
// URL-bearing attribute on each event.
func (s sanitizeReadOnlySpan) Events() []trace.Event {
	original := s.ReadOnlySpan.Events()
	if len(original) == 0 {
		return original
	}
	out := make([]trace.Event, len(original))
	for i, e := range original {
		e.Attributes = redactURLAttrs(e.Attributes)
		out[i] = e
	}
	return out
}

// Links returns the span links with credentials stripped from any URL-bearing
// attribute on each link.
func (s sanitizeReadOnlySpan) Links() []trace.Link {
	original := s.ReadOnlySpan.Links()
	if len(original) == 0 {
		return original
	}
	out := make([]trace.Link, len(original))
	for i, l := range original {
		l.Attributes = redactURLAttrs(l.Attributes)
		out[i] = l
	}
	return out
}

// redactURLAttrs returns a copy of attrs with credentials stripped from any
// URL-bearing attribute key. The original slice is returned unchanged when it
// is empty or contains no redactable values.
func redactURLAttrs(attrs []attribute.KeyValue) []attribute.KeyValue {
	if len(attrs) == 0 {
		return attrs
	}
	out := make([]attribute.KeyValue, len(attrs))
	for i, kv := range attrs {
		if _, ok := urlAttrKeys[string(kv.Key)]; ok && kv.Value.Type() == attribute.STRING {
			out[i] = attribute.String(string(kv.Key), redactURL(kv.Value.AsString()))
			continue
		}
		out[i] = kv
	}
	return out
}
