package otel

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// testResource builds a minimal resource for provider tests.
func testResource(t *testing.T) *resource.Resource {
	t.Helper()
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(attribute.String("service.name", "test")),
	)
	require.NoError(t, err)
	return res
}

func forceFlush(t *testing.T, tp any) {
	t.Helper()
	sdk, ok := tp.(*sdktrace.TracerProvider)
	if ok {
		require.NoError(t, sdk.ForceFlush(context.Background()))
	}
}

func TestNewProviders_AllNoop(t *testing.T) {
	t.Parallel()

	// No exporters + nil resource → all providers are noop.
	p, err := NewProviders(nil, Exporters{})
	require.NoError(t, err)
	require.NotNil(t, p)
	require.NotNil(t, p.TracerProvider)
	require.NotNil(t, p.MeterProvider)
	require.NotNil(t, p.Instruments)
	require.NotNil(t, p.LoggerProvider)
	require.Equal(t, io.Discard, p.LogWriter)
	require.NoError(t, p.Shutdown(context.Background()))
}

func TestNewProviders_WithTracesExporter(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	res := testResource(t)
	p, err := NewProviders(res, Exporters{Traces: mem})
	require.NoError(t, err)
	require.NotNil(t, p.TracerProvider)

	tracer := p.TracerProvider.Tracer("test")
	_, span := tracer.Start(context.Background(), "op")
	span.End()
	forceFlush(t, p.TracerProvider)

	spans := mem.GetSpans()
	require.Len(t, spans, 1)
	require.Equal(t, "op", spans[0].Name)
	require.NoError(t, p.Shutdown(context.Background()))
}

func TestNewProviders_ShutdownIdempotent(t *testing.T) {
	t.Parallel()

	mem := tracetest.NewInMemoryExporter()
	res := testResource(t)
	p, err := NewProviders(res, Exporters{Traces: mem})
	require.NoError(t, err)

	require.NoError(t, p.Shutdown(context.Background()))
	// A second shutdown is a no-op (no panic, no error).
	require.NoError(t, p.Shutdown(context.Background()))
}

func TestSetup_DisabledByDefault(t *testing.T) {
	// Non-parallel: Setup would touch globals when enabled; disabled path is
	// global-free, but keep it sequential for clarity.
	shutdown, logWriter, err := Setup(context.Background(), BuildInfo{Commit: "abc", Date: "2026-01-02"})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.Equal(t, io.Discard, logWriter)
	require.NoError(t, shutdown())
}

// TestSetup_EnabledReturnsValidWriter verifies the contract that Setup NEVER
// returns nil shutdown or logWriter — so a caller passing logWriter into
// io.MultiWriter never panics on the first log line, even on the enabled path.
func TestSetup_EnabledReturnsValidWriter(t *testing.T) {
	// stdout exporter avoids any network dependency and makes shutdown safe.
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER", "stdout")

	shutdown, logWriter, err := Setup(context.Background(), BuildInfo{})
	require.NoError(t, err)
	require.NotNil(t, shutdown, "shutdown must never be nil")
	require.NotNil(t, logWriter, "logWriter must never be nil")
	require.NotEqual(t, io.Discard, logWriter)
	// shutdown must be idempotent and collector-independent here.
	require.NoError(t, shutdown())
}

func TestParseConfig_DisabledDefault(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "")
	cfg := parseConfig()
	require.False(t, cfg.enabled)
	require.False(t, cfg.tracesEnabled)
}

func TestParseConfig_PerSignalDisable(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_LOGS_ENABLED", "false")

	cfg := parseConfig()
	require.True(t, cfg.enabled)
	require.True(t, cfg.tracesEnabled)
	require.True(t, cfg.metricsEnabled)
	require.False(t, cfg.logsEnabled)
}

func TestParseConfig_StdoutExporter(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER", "stdout")

	cfg := parseConfig()
	require.True(t, cfg.enabled)
	require.True(t, cfg.exporterStdout)
}

func TestInstruments_NonNil(t *testing.T) {
	t.Parallel()

	inst := Instruments()
	require.NotNil(t, inst)
	require.NotNil(t, inst.CommandsTotal)
	require.NotNil(t, inst.CommandDuration)
	require.NotNil(t, inst.RateLimitedTotal)
	require.NotNil(t, inst.GenAITokenUsage)
}
