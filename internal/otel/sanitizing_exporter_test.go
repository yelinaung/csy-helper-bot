package otel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
		attribute.String("url.full", "https://finnhub.io/api/v1/quote?symbol=AAPL&token=secret"),
	})
	require.Len(t, stubs, 1)

	var full string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == "url.full" {
			full = kv.Value.AsString()
		}
	}
	require.Contains(t, full, "token=<redacted>")
	require.NotContains(t, full, "secret")
}

func TestSanitizer_RedactsTelegramBotToken(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String("url.full", "https://api.telegram.org/file/bot123:abc-def/file.jpg"),
	})
	require.Len(t, stubs, 1)

	var full string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == "url.full" {
			full = kv.Value.AsString()
		}
	}
	require.Contains(t, full, "bot<redacted>")
	require.NotContains(t, full, "123:abc-def")
}

func TestSanitizer_RedactsLegacyHttpUrlKey(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String("http.url", "https://finnhub.io/api/v1/quote?token=secret"),
	})
	require.Len(t, stubs, 1)

	var httpURL string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == "http.url" {
			httpURL = kv.Value.AsString()
		}
	}
	require.Contains(t, httpURL, "token=<redacted>")
	require.NotContains(t, httpURL, "secret")
}

func TestSanitizer_RedactsBothUrlKeys(t *testing.T) {
	t.Parallel()

	stubs := exportAndCollect(t, []attribute.KeyValue{
		attribute.String("url.full", "https://finnhub.io/api/v1/quote?token=secret1"),
		attribute.String("http.url", "https://finnhub.io/api/v1/quote?token=secret2"),
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
		attribute.String("url.full", clean),
	})
	require.Len(t, stubs, 1)

	var full string
	for _, kv := range stubs[0].Attributes {
		if string(kv.Key) == "url.full" {
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
