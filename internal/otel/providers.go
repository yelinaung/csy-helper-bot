package otel

import (
	"context"
	"io"

	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Providers bundles the three OTel providers, the domain instruments, the
// zerolog log writer, and a shutdown hook. Tests construct this directly with
// in-memory exporters via NewProviders and never touch the global providers.
type Providers struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
	Instruments    *InstrumentSet
	LogWriter      io.Writer

	shutdown func(context.Context) error
}

// Exporters holds the three signal exporters. Injected so tests pass in-memory
// exporters and production passes OTLP/HTTP exporters. A nil exporter for a
// signal installs a noop provider for that signal.
type Exporters struct {
	Traces  sdktrace.SpanExporter
	Metrics sdkmetric.Exporter
	Logs    sdklog.Exporter
}

// NewProviders builds Providers from res + injected exporters. Instruments are
// constructed from the MeterProvider here, so they are always bound to the
// correct (test or production) provider. When an exporter is nil the
// corresponding provider is noop and no data is exported.
func NewProviders(res *resource.Resource, exp Exporters) (*Providers, error) {
	p := &Providers{LogWriter: io.Discard}
	var shutdowns []func(context.Context) error

	switch {
	case exp.Traces != nil && res != nil:
		tp := newTracerProvider(res, exp.Traces)
		p.TracerProvider = tp
		shutdowns = append(shutdowns, tp.Shutdown)
	default:
		p.TracerProvider = tracenoop.NewTracerProvider()
	}

	switch {
	case exp.Metrics != nil && res != nil:
		mp := newMeterProvider(res, exp.Metrics)
		p.MeterProvider = mp
		shutdowns = append(shutdowns, mp.Shutdown)
	default:
		p.MeterProvider = noopMeterProvider()
	}

	instruments, err := newInstruments(p.MeterProvider.Meter(instrumentScope))
	if err != nil {
		return nil, err
	}
	p.Instruments = instruments

	switch {
	case exp.Logs != nil && res != nil:
		lp := newLoggerProvider(res, exp.Logs)
		p.LoggerProvider = lp
		p.LogWriter = newZerologBridge(lp)
		shutdowns = append(shutdowns, lp.Shutdown)
	default:
		p.LoggerProvider = sdklog.NewLoggerProvider()
	}

	p.shutdown = joinShutdown(shutdowns)
	return p, nil
}

// Shutdown flushes buffered signals and shuts down the exporters. It is safe
// to call multiple times; subsequent calls are no-ops.
func (p *Providers) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

// joinShutdown returns a one-shot shutdown that runs each closer in order and
// returns the first error. Subsequent calls return nil.
func joinShutdown(closers []func(context.Context) error) func(context.Context) error {
	var ran bool
	return func(ctx context.Context) error {
		if ran {
			return nil
		}
		ran = true
		var firstErr error
		for _, c := range closers {
			if err := c(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
}

// instrumentScope is the meter/tracer scope name.
const instrumentScope = "csy-helper-bot/otel"

// defaultInstruments is the package-level instrument set used by handlers. It
// starts as noop so the bot works without Setup; Setup replaces it with the
// production-backed instruments.
var defaultInstruments = noopInstrumentSet()

// setDefaultInstruments swaps in the production instruments. Called by Setup.
func setDefaultInstruments(i *InstrumentSet) {
	if i != nil {
		defaultInstruments = i
	}
}

// Instruments returns the cached domain instruments. They are noop until Setup
// installs production providers, so callers always get valid handles.
func Instruments() *InstrumentSet {
	return defaultInstruments
}

// installGlobals wires the providers into the OTel globals so library code
// using otel.Tracer/otel.Meter picks them up. The setters are injected so this
// package does not import the top-level otel package (it lives in setup.go).
func (p *Providers) installGlobals(setTracer func(trace.TracerProvider), setMeter func(metric.MeterProvider)) {
	if p == nil {
		return
	}
	if p.TracerProvider != nil && setTracer != nil {
		setTracer(p.TracerProvider)
	}
	if p.MeterProvider != nil && setMeter != nil {
		setMeter(p.MeterProvider)
	}
}
