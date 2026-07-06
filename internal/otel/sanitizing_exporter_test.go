package otel

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// spanFromStub builds a ReadOnlySpan snapshot carrying the given attributes so
// the sanitizer can be exercised. The tracetest SpanStub → Span conversion is
// done via the embedded exporter round-trip.
func exportAndCollect(t *testing.T, attrs []attribute.KeyValue) []tracetest.SpanStub {
	t.Helper()
	require := require.New(t)

	mem := tracetest.NewInMemoryExporter()
	sanitized := newSanitizingExporter(mem)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(sanitized),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test.span")
	span.SetAttributes(attrs...)
	span.SetStatus(codes.Ok, "")
	span.End()

	require.NoError(tp.ForceFlush(context.Background()))
	return mem.GetSpans()
}

func TestSanitizer_StripsFinnhubToken(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String(attrURLFull, "https://finnhub.io/api/v1/quote?symbol=AAPL&token=secret"),
	})
	require.Len(t, stubs, 1)

	var full string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == attrURLFull {
			full = kv.Value.AsString()
		}
	}
	require.Contains(t, full, "token=<redacted>")
	require.NotContains(t, full, "secret")
}

func TestSanitizer_RedactsTelegramBotToken(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String(attrURLFull, "https://api.telegram.org/file/bot123:abc-def/file.jpg"),
	})
	require.Len(t, stubs, 1)

	var full string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == attrURLFull {
			full = kv.Value.AsString()
		}
	}
	require.Contains(t, full, "bot<redacted>")
	require.NotContains(t, full, "123:abc-def")
}

func TestSanitizer_RedactsLegacyHttpUrlKey(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String(attrHTTPURL, "https://finnhub.io/api/v1/quote?token=secret"),
	})
	require.Len(t, stubs, 1)

	var httpURL string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == attrHTTPURL {
			httpURL = kv.Value.AsString()
		}
	}
	require.Contains(t, httpURL, "token=<redacted>")
	require.NotContains(t, httpURL, "secret")
}

func TestSanitizer_RedactsBothUrlKeys(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String(attrURLFull, "https://finnhub.io/api/v1/quote?token=secret1"),
		attribute.String(attrHTTPURL, "https://finnhub.io/api/v1/quote?token=secret2"),
	})
	require.Len(t, stubs, 1)

	for _, kv := range stubs[0].Attributes {
		require.NotContains(t, kv.Value.AsString(), "secret1")
		require.NotContains(t, kv.Value.AsString(), "secret2")
	}
}

func TestSanitizer_PassesCleanURLs(t *testing.T) {
	t.Parallel()

	clean := "https://example.com/api/v1/quote?symbol=AAPL"
	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String(attrURLFull, clean),
	})
	require.Len(t, stubs, 1)

	var full string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == attrURLFull {
			full = kv.Value.AsString()
		}
	}
	require.Equal(t, clean, full)
}

func TestSanitizer_PassesNonURLAttributes(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String("symbol", "AAPL"),
		attribute.Int64("http.response.status_code", 200),
	})
	require.Len(t, stubs, 1)

	attrMap := map[string]any{}
	for _, kv := range stubs[0].Attributes {
		attrMap[string(kv.Key)] = kv.Value.AsInterface()
	}
	require.Equal(t, "AAPL", attrMap["symbol"])
	require.EqualValues(t, 200, attrMap["http.response.status_code"])
}

// exportAndCollectEvents builds a span that records an event and a link
// carrying URL attributes, then returns the exported (sanitized) stub so the
// redaction of event/link attributes can be asserted.
func exportAndCollectEvents(t *testing.T) []tracetest.SpanStub {
	t.Helper()
	require := require.New(t)

	mem := tracetest.NewInMemoryExporter()
	sanitized := newSanitizingExporter(mem)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(sanitized))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	// Start a parent span to obtain a valid SpanContext for the link.
	parentCtx, parentSpan := tracer.Start(context.Background(), "parent")
	parentSpan.End()

	_, span := tracer.Start(parentCtx, "test.span")
	span.AddEvent("download", trace.WithAttributes(
		attribute.String(attrURLFull, "https://finnhub.io/api/v1/quote?token=secret"),
	))
	span.AddLink(trace.Link{
		SpanContext: parentSpan.SpanContext(),
		Attributes: []attribute.KeyValue{
			attribute.String(attrHTTPURL, "https://api.telegram.org/file/bot123:abc/x"),
		},
	})
	span.SetStatus(codes.Ok, "")
	span.End()

	require.NoError(tp.ForceFlush(context.Background()))
	return mem.GetSpans()
}

func TestSanitizer_RedactsStatusDescription(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	sanitized := newSanitizingExporter(mem)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(sanitized))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test.span")
	span.SetStatus(codes.Error, `Get "https://finnhub.io/api/v1/quote?token=secret": dial tcp`)
	span.End()

	require.NoError(t, tp.ForceFlush(context.Background()))
	stubs := mem.GetSpans()
	require.Len(t, stubs, 1)
	require.Contains(t, stubs[0].Status.Description, "token=<redacted>")
	require.NotContains(t, stubs[0].Status.Description, "secret")
}

func TestSanitizer_RedactsExceptionMessageEvent(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	sanitized := newSanitizingExporter(mem)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(sanitized))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test.span")
	span.RecordError(errors.New(`Get "https://api.telegram.org/file/bot123456:ABC-DEF_ghi/file.jpg": EOF`))
	span.End()

	require.NoError(t, tp.ForceFlush(context.Background()))
	stubs := mem.GetSpans()
	require.Len(t, stubs, 1)
	require.NotEmpty(t, stubs[0].Events)
	found := false
	for _, kv := range stubs[0].Events[0].Attributes {
		if string(kv.Key) == "exception.message" {
			found = true
			require.Contains(t, kv.Value.AsString(), "bot<redacted>")
			require.NotContains(t, kv.Value.AsString(), "123456:ABC-DEF_ghi")
		}
	}
	require.True(t, found, "exception.message attribute not exported")
}

func TestSanitizer_DoesNotRedactStatusDescriptionWithoutURL(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	sanitized := newSanitizingExporter(mem)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(sanitized))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test.span.no_url_status")
	const desc = "temporary failure"
	span.SetStatus(codes.Error, desc)
	span.End()

	require.NoError(t, tp.ForceFlush(context.Background()))
	stubs := mem.GetSpans()
	require.Len(t, stubs, 1)
	require.Equal(t, desc, stubs[0].Status.Description)
}

func TestSanitizer_DoesNotRedactExceptionMessageWithoutURL(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	sanitized := newSanitizingExporter(mem)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(sanitized))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test.span.no_url_exception")
	const msg = "temporary failure"
	span.RecordError(errors.New(msg))
	span.End()

	require.NoError(t, tp.ForceFlush(context.Background()))
	stubs := mem.GetSpans()
	require.Len(t, stubs, 1)
	require.NotEmpty(t, stubs[0].Events)

	found := false
	for _, kv := range stubs[0].Events[0].Attributes {
		if string(kv.Key) == "exception.message" {
			found = true
			require.Equal(t, msg, kv.Value.AsString())
		}
	}
	require.True(t, found, "exception.message attribute not exported")
}

func TestRedactAttributeString_UrlKeyNonStringValueUnchanged(t *testing.T) {
	t.Parallel()

	redacted, ok := redactAttributeString(attribute.Int(attrURLFull, 42))

	require.False(t, ok)
	require.Empty(t, redacted)
}

func TestRedactAttributeString_SensitiveKeySafeStringUnchanged(t *testing.T) {
	t.Parallel()

	const msg = "plain text without URLs or tokens"
	redacted, ok := redactAttributeString(attribute.String("exception.message", msg))

	require.True(t, ok)
	require.Equal(t, msg, redacted)
}

func TestSanitizer_RedactsEventAndLinkAttributes(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollectEvents(t)
	// The parent and test.span are both exported; find the one with the event.
	var span tracetest.SpanStub
	for _, s := range stubs {
		if s.Name == "test.span" {
			span = s
		}
	}
	require.Equal(t, "test.span", span.Name, "test.span stub not exported")

	for _, ev := range span.Events {
		for _, kv := range ev.Attributes {
			if string(kv.Key) == attrURLFull {
				require.Contains(t, kv.Value.AsString(), "<redacted>")
				require.NotContains(t, kv.Value.AsString(), "secret")
			}
		}
	}
	for _, lk := range span.Links {
		for _, kv := range lk.Attributes {
			if string(kv.Key) == attrHTTPURL {
				require.Contains(t, kv.Value.AsString(), "bot<redacted>")
				require.NotContains(t, kv.Value.AsString(), "123:abc")
			}
		}
	}
}
