package otel

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// zerologLevelValues maps zerolog level strings to OTel log severities.
var zerologLevelValues = map[string]otellog.Severity{
	"trace": otellog.SeverityTrace,
	"debug": otellog.SeverityDebug,
	"info":  otellog.SeverityInfo,
	"warn":  otellog.SeverityWarn,
	"error": otellog.SeverityError,
	"fatal": otellog.SeverityFatal,
	"panic": otellog.SeverityFatal,
}

// newZerologBridge returns an io.Writer that parses each zerolog JSON line and
// emits an OTel log record via lp. It is safe for concurrent use. Each Write
// may contain multiple newline-delimited JSON objects.
func newZerologBridge(lp *sdklog.LoggerProvider) io.Writer {
	logger := lp.Logger(instrumentScope)
	return &otelLogWriter{logger: logger}
}

// otelLogWriter is an io.Writer that parses zerolog JSON lines into OTel log
// records. Trace correlation is not possible from the writer alone because
// Write receives bytes, not context.Context (see the integration plan, v2).
type otelLogWriter struct {
	logger otellog.Logger
}

func (w *otelLogWriter) Write(p []byte) (int, error) {
	// Always report the full write so zerolog does not retry.
	n := len(p)

	for line := range strings.Lines(string(p)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		w.emitLine(line)
	}
	return n, nil
}

// emitLine decodes one zerolog JSON line into an OTel log record.
func (w *otelLogWriter) emitLine(line string) {
	// UseNumber preserves integer fidelity for large IDs/timestamps that lose
	// precision as float64; toLogAttribute handles json.Number explicitly.
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()

	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		// Non-JSON input is dropped (defense-in-depth: never panic).
		return
	}

	var record otellog.Record
	record.SetObservedTimestamp(time.Now())

	if level, ok := fields["level"].(string); ok {
		record.SetSeverityText(level)
		record.SetSeverity(zerologLevelValues[strings.ToLower(level)])
	}

	if t, ok := parseZerologTime(fields["time"]); ok {
		record.SetTimestamp(t)
	}

	if message, ok := fields["message"].(string); ok {
		record.SetBody(otellog.StringValue(message))
	}

	attrs := make([]otellog.KeyValue, 0, len(fields))
	for k, v := range fields {
		if isReservedZerologKey(k) {
			continue
		}
		attrs = append(attrs, toLogAttribute(k, v))
	}
	if len(attrs) > 0 {
		record.AddAttributes(attrs...)
	}

	w.logger.Emit(context.Background(), record)
}

// reservedZerologKeys are handled specially and excluded from attributes.
var reservedZerologKeys = map[string]struct{}{
	"time":    {},
	"level":   {},
	"message": {},
}

func isReservedZerologKey(k string) bool {
	_, ok := reservedZerologKeys[k]
	return ok
}

// parseZerologTime parses a zerolog time field (RFC3339 by default in this app).
func parseZerologTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// toLogAttribute best-effort types a zerolog field value into an OTel log
// KeyValue. URL-bearing keys are redacted via the shared redactURL helper.
func toLogAttribute(k string, v any) otellog.KeyValue {
	if _, isURLKey := logURLAttrKeys[strings.ToLower(k)]; isURLKey {
		if s, ok := v.(string); ok {
			return otellog.String(k, redactURL(s))
		}
	}

	switch val := v.(type) {
	case string:
		return otellog.String(k, val)
	case bool:
		return otellog.Bool(k, val)
	case float64:
		return otellog.Float64(k, val)
	case json.Number:
		// Prefer int64 for whole numbers (preserves large IDs/timestamps that
		// lose precision as float64); fall back to float64, then the raw text.
		if i, err := val.Int64(); err == nil {
			return otellog.Int64(k, i)
		}
		if f, err := val.Float64(); err == nil {
			return otellog.Float64(k, f)
		}
		return otellog.String(k, val.String())
	case nil:
		return otellog.String(k, "")
	default:
		raw, err := json.Marshal(val)
		if err != nil {
			return otellog.String(k, "")
		}
		return otellog.String(k, string(raw))
	}
}
