package otel

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// capturingLogExporter collects emitted log records for assertions.
type capturingLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *capturingLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cloned := make([]sdklog.Record, 0, len(records))
	for i := range records {
		cloned = append(cloned, records[i].Clone())
	}
	e.records = append(e.records, cloned...)
	return nil
}

func (e *capturingLogExporter) ForceFlush(_ context.Context) error { return nil }

func (e *capturingLogExporter) Shutdown(_ context.Context) error { return nil }

func (e *capturingLogExporter) snapshot() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]sdklog.Record, len(e.records))
	copy(out, e.records)
	return out
}

// newBridgeWithCapture wires a zerolog bridge to a fresh logger provider and
// returns the writer plus the capturing exporter.
func newBridgeWithCapture(t *testing.T) (*capturingLogExporter, *otelLogWriter) {
	t.Helper()
	exp := &capturingLogExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	w, ok := newZerologBridge(lp).(*otelLogWriter)
	require.True(t, ok, "bridge should be *otelLogWriter")
	return exp, w
}

func attributeMap(r *sdklog.Record) map[string]string {
	out := map[string]string{}
	r.WalkAttributes(func(kv otellog.KeyValue) bool {
		switch kv.Value.Kind() {
		case otellog.KindBool:
			out[kv.Key] = strconv.FormatBool(kv.Value.AsBool())
		case otellog.KindInt64:
			out[kv.Key] = strconv.FormatInt(kv.Value.AsInt64(), 10)
		case otellog.KindFloat64:
			out[kv.Key] = strconv.FormatFloat(kv.Value.AsFloat64(), 'f', -1, 64)
		case otellog.KindEmpty, otellog.KindString, otellog.KindBytes, otellog.KindSlice, otellog.KindMap:
			out[kv.Key] = kv.Value.AsString()
		}
		return true
	})
	return out
}

func TestZerologBridge_LevelMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level    string
		severity otellog.Severity
	}{
		{"trace", otellog.SeverityTrace},
		{"debug", otellog.SeverityDebug},
		{"info", otellog.SeverityInfo},
		{"warn", otellog.SeverityWarn},
		{"error", otellog.SeverityError},
		{"fatal", otellog.SeverityFatal},
		{"panic", otellog.SeverityFatal},
	}
	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			t.Parallel()
			exp, w := newBridgeWithCapture(t)
			line := `{"level":"` + tc.level + `","message":"hi","time":"2026-01-02T15:04:05Z"}` + "\n"
			_, _ = w.Write([]byte(line))

			// The simple processor exports synchronously, so records are ready.
			records := exp.snapshot()
			require.Len(t, records, 1)
			require.Equal(t, tc.severity, records[0].Severity())
			require.Equal(t, tc.level, records[0].SeverityText())
		})
	}
}

func TestZerologBridge_BodyAndTimestamp(t *testing.T) {
	t.Parallel()

	exp, w := newBridgeWithCapture(t)
	_, _ = w.Write([]byte(`{"level":"info","message":"hello","time":"2026-01-02T15:04:05Z"}` + "\n"))

	records := exp.snapshot()
	require.Len(t, records, 1)
	require.Equal(t, "hello", records[0].Body().AsString())
	want, err := time.Parse(time.RFC3339, "2026-01-02T15:04:05Z")
	require.NoError(t, err)
	require.Equal(t, want, records[0].Timestamp())
}

func TestZerologBridge_Attributes(t *testing.T) {
	t.Parallel()

	exp, w := newBridgeWithCapture(t)
	_, _ = w.Write([]byte(`{"level":"info","message":"m","symbol":"AAPL","price":150.5,"ok":true,"nested":{"a":1}}` + "\n"))

	records := exp.snapshot()
	require.Len(t, records, 1)
	attrs := attributeMap(&records[0])
	require.Equal(t, "AAPL", attrs["symbol"])
	require.Equal(t, "150.5", attrs["price"])
	require.Equal(t, "true", attrs["ok"])
	require.Contains(t, attrs["nested"], `"a":1`)
}

func TestZerologBridge_IntegerPreservesInt64(t *testing.T) {
	t.Parallel()

	// Large IDs must not lose precision through float64. With UseNumber, whole
	// numbers decode as json.Number and are emitted as int64 attributes.
	exp, w := newBridgeWithCapture(t)
	bigID := "9007199254740993" // 2^53 + 1, not representable as float64
	_, _ = w.Write([]byte(`{"level":"info","message":"m","user_id":` + bigID + `}` + "\n"))

	records := exp.snapshot()
	require.Len(t, records, 1)
	require.Equal(t, otellog.KindInt64, logAttrKind(&records[0], "user_id"))
	require.Equal(t, bigID, attributeMap(&records[0])["user_id"])
}

// logAttrKind returns the Kind of the named attribute, or KindEmpty if absent.
func logAttrKind(r *sdklog.Record, key string) otellog.Kind {
	var kind otellog.Kind
	r.WalkAttributes(func(kv otellog.KeyValue) bool {
		if kv.Key == key {
			kind = kv.Value.Kind()
			return false
		}
		return true
	})
	return kind
}

func TestZerologBridge_Multiline(t *testing.T) {
	t.Parallel()

	exp, w := newBridgeWithCapture(t)
	input := `{"level":"info","message":"first"}` + "\n" + `{"level":"error","message":"second"}` + "\n"
	_, _ = w.Write([]byte(input))

	records := exp.snapshot()
	require.Len(t, records, 2)
	require.Equal(t, "first", records[0].Body().AsString())
	require.Equal(t, "second", records[1].Body().AsString())
}

func TestZerologBridge_EmptyAndGarbage(t *testing.T) {
	t.Parallel()

	exp, w := newBridgeWithCapture(t)
	require.NotPanics(t, func() {
		_, _ = w.Write([]byte(""))
		_, _ = w.Write([]byte("\n\n"))
		_, _ = w.Write([]byte("not json at all"))
	})

	require.Empty(t, exp.snapshot())
}

func TestZerologBridge_RedactsErrorAttribute(t *testing.T) {
	t.Parallel()

	exp, w := newBridgeWithCapture(t)
	line := `{"level":"error","message":"fetch failed","error":"Get \"https://finnhub.io/api/v1/quote?token=secret\": dial tcp"}` + "\n"
	_, _ = w.Write([]byte(line))

	records := exp.snapshot()
	require.Len(t, records, 1)
	attrs := attributeMap(&records[0])
	require.Contains(t, attrs["error"], "token=<redacted>")
	require.NotContains(t, attrs["error"], "secret")
}

func TestZerologBridge_RedactsNonStringErrorAttribute(t *testing.T) {
	t.Parallel()

	exp, w := newBridgeWithCapture(t)
	line := `{"level":"error","message":"fetch failed","error":{"message":"Get \"HTTPS://finnhub.io/api/v1/quote?token=secret\": dial tcp"}}` + "\n"
	_, _ = w.Write([]byte(line))

	records := exp.snapshot()
	require.Len(t, records, 1)
	attrs := attributeMap(&records[0])
	require.Contains(t, attrs["error"], "token=<redacted>")
	require.NotContains(t, attrs["error"], "secret")
}

func TestZerologBridge_RedactsURLAttributes(t *testing.T) {
	t.Parallel()

	tests := []string{"url", attrURLFull, attrHTTPURL, "request.url"}
	for _, key := range tests {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			exp, w := newBridgeWithCapture(t)
			line := `{"level":"error","message":"fetch failed","` + key + `":"https://finnhub.io/api/v1/quote?token=secret"}` + "\n"
			_, _ = w.Write([]byte(line))

			records := exp.snapshot()
			require.Len(t, records, 1)
			attrs := attributeMap(&records[0])
			require.Contains(t, attrs[key], "<redacted>")
			require.NotContains(t, attrs[key], "secret")
		})
	}
}
