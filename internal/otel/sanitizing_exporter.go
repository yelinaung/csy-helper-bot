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

// Status returns the span status with credentials stripped from the description.
func (s sanitizeReadOnlySpan) Status() trace.Status {
	status := s.ReadOnlySpan.Status()
	status.Description = RedactSensitiveText(status.Description)
	return status
}

// redactURLAttrs returns a copy of attrs with credentials stripped from any
// URL-bearing attribute key or sensitive error-message attribute. The original
// slice is returned unchanged when it is empty or contains no redactable values.
func redactURLAttrs(attrs []attribute.KeyValue) []attribute.KeyValue {
	if len(attrs) == 0 {
		return attrs
	}
	var out []attribute.KeyValue
	for i, kv := range attrs {
		redacted, ok := redactAttributeString(kv)
		if ok {
			if out == nil {
				out = make([]attribute.KeyValue, len(attrs))
				copy(out, attrs[:i])
			}
			out[i] = attribute.String(string(kv.Key), redacted)
			continue
		}
		if out != nil {
			out[i] = kv
		}
	}
	if out == nil {
		return attrs
	}
	return out
}

func redactAttributeString(kv attribute.KeyValue) (string, bool) {
	if kv.Value.Type() != attribute.STRING {
		return "", false
	}
	key := string(kv.Key)
	if _, ok := urlAttrKeys[key]; ok {
		return redactURL(kv.Value.AsString()), true
	}
	if _, ok := spanSensitiveTextAttrKeys[key]; ok {
		return RedactSensitiveText(kv.Value.AsString()), true
	}
	return "", false
}
