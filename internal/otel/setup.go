package otel

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otlpmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otlptracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	stdoutlog "go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	stdoutmetric "go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
)

// BuildInfo carries ldflags-derived build metadata for the resource.
type BuildInfo struct {
	Commit string
	Date   string
}

// config holds parsed OTel configuration.
type config struct {
	enabled        bool
	tracesEnabled  bool
	metricsEnabled bool
	logsEnabled    bool
	exporterStdout bool
}

const (
	defaultServiceName = "csy-helper-bot"
	shutdownTimeout    = 5 * time.Second
)

func parseConfig() config {
	master := strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_ENABLED")), "true")
	c := config{
		enabled:        master,
		tracesEnabled:  master,
		metricsEnabled: master,
		logsEnabled:    master,
		exporterStdout: strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_EXPORTER")), "stdout"),
	}
	if v, ok := os.LookupEnv("OTEL_TRACES_ENABLED"); ok {
		c.tracesEnabled = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := os.LookupEnv("OTEL_METRICS_ENABLED"); ok {
		c.metricsEnabled = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v, ok := os.LookupEnv("OTEL_LOGS_ENABLED"); ok {
		c.logsEnabled = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return c
}

// buildResource assembles the shared resource from env + build info.
func buildResource(_ context.Context, info BuildInfo) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", serviceName()),
	}
	if info.Commit != "" {
		attrs = append(attrs, attribute.String("service.version", info.Commit))
	}
	if info.Date != "" {
		attrs = append(attrs, attribute.String("build.date", info.Date))
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		attrs = append(attrs, attribute.String("host.name", host))
	}
	attrs = append(attrs, attribute.Int64("process.pid", int64(os.Getpid())))

	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithProcessPID(),
	)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func serviceName() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		return v
	}
	return defaultServiceName
}

// Setup is the production entry point: parses env, builds exporters (OTLP/HTTP
// or stdout), wraps the trace exporter in the sanitizer, calls NewProviders,
// installs the providers into the OTel globals, and returns a shutdown func +
// the zerolog log writer. When OTEL_ENABLED is not "true", it installs noop
// providers and returns io.Discard, so callers always get valid values.
func Setup(ctx context.Context, info BuildInfo) (shutdown func() error, logWriter io.Writer, err error) {
	// Defaults guarantee callers always receive valid values even on error, so
	// the process can continue without telemetry rather than panic when the
	// writer/shutdown are used unconditionally.
	shutdown = func() error { return nil }
	logWriter = io.Discard

	cfg := parseConfig()
	if !cfg.enabled {
		return shutdown, logWriter, nil
	}

	res, err := buildResource(ctx, info)
	if err != nil {
		return shutdown, logWriter, err
	}

	exp, err := buildExporters(ctx, cfg)
	if err != nil {
		return shutdown, logWriter, err
	}

	// Wrap the trace exporter in the sanitizer (P0) before wiring it in.
	if exp.Traces != nil {
		exp.Traces = newSanitizingExporter(exp.Traces)
	}

	providers, err := NewProviders(res, exp)
	if err != nil {
		return shutdown, logWriter, err
	}

	providers.installGlobals(otel.SetTracerProvider, otel.SetMeterProvider)
	setDefaultInstruments(providers.Instruments)

	logWriter = providers.LogWriter

	var once sync.Once
	shutdown = func() error {
		var shutdownErr error
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			shutdownErr = providers.Shutdown(shutdownCtx)
		})
		return shutdownErr
	}
	return shutdown, logWriter, nil
}

// buildExporters constructs the signal exporters based on cfg. Disabled
// signals get a nil exporter (NewProviders installs a noop provider for them).
func buildExporters(ctx context.Context, cfg config) (Exporters, error) {
	var exp Exporters
	var err error

	if cfg.tracesEnabled {
		if cfg.exporterStdout {
			exp.Traces, err = stdouttrace.New()
		} else {
			exp.Traces, err = otlptracehttp.New(ctx)
		}
		if err != nil {
			return exp, err
		}
	}

	if cfg.metricsEnabled {
		if cfg.exporterStdout {
			exp.Metrics, err = stdoutmetric.New()
		} else {
			exp.Metrics, err = otlpmetrichttp.New(ctx)
		}
		if err != nil {
			return exp, err
		}
	}

	if cfg.logsEnabled {
		if cfg.exporterStdout {
			exp.Logs, err = stdoutlog.New()
		} else {
			exp.Logs, err = otlploghttp.New(ctx)
		}
		if err != nil {
			return exp, err
		}
	}

	return exp, nil
}
