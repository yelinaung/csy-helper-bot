package otel

import (
	"time"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// metricExportInterval is the default periodic reader export interval.
const metricExportInterval = 30 * time.Second

// newMeterProvider builds a meter provider with a periodic reader over the
// given exporter and resource.
func newMeterProvider(res *resource.Resource, exporter sdkmetric.Exporter) *sdkmetric.MeterProvider {
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(metricExportInterval))
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
}

// InstrumentSet holds the cached domain metric handles. Built once from a
// meter via newInstruments. When telemetry is disabled, a noop-meter-backed
// set is returned so callers always get valid instruments.
type InstrumentSet struct {
	CommandsTotal    metric.Int64Counter
	CommandDuration  metric.Float64Histogram
	RateLimitedTotal metric.Int64Counter
	GenAITokenUsage  metric.Float64Histogram
}

// GenAI token types.
const (
	GenAITokenTypeInput  = "input"
	GenAITokenTypeOutput = "output"
)

// newInstruments constructs the domain instruments from the given meter.
func newInstruments(meter metric.Meter) (*InstrumentSet, error) {
	commandsTotal, err := meter.Int64Counter(
		"bot.commands.total",
		metric.WithUnit("1"),
		metric.WithDescription("Number of bot commands handled."),
	)
	if err != nil {
		return nil, err
	}

	commandDuration, err := meter.Float64Histogram(
		"bot.command.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Bot command handler duration in seconds."),
	)
	if err != nil {
		return nil, err
	}

	rateLimitedTotal, err := meter.Int64Counter(
		"bot.rate_limited.total",
		metric.WithUnit("1"),
		metric.WithDescription("Number of rate-limited requests."),
	)
	if err != nil {
		return nil, err
	}

	// gen_ai.client.token.usage is a Histogram per the OTel GenAI semconv.
	genAITokenUsage, err := meter.Float64Histogram(
		"gen_ai.client.token.usage",
		metric.WithUnit("{token}"),
		metric.WithDescription("Measures number of input and output tokens used."),
	)
	if err != nil {
		return nil, err
	}

	return &InstrumentSet{
		CommandsTotal:    commandsTotal,
		CommandDuration:  commandDuration,
		RateLimitedTotal: rateLimitedTotal,
		GenAITokenUsage:  genAITokenUsage,
	}, nil
}

// noopMeterProvider returns a noop meter provider used when metrics export is
// disabled.
func noopMeterProvider() metric.MeterProvider {
	return metricnoop.NewMeterProvider()
}

// noopInstrumentSet builds instruments from the global noop meter so callers
// get valid handles when telemetry is disabled.
func noopInstrumentSet() *InstrumentSet {
	meter := metricnoop.NewMeterProvider().Meter(instrumentScope)
	inst, err := newInstruments(meter)
	if err != nil {
		// newInstruments only fails on instrument name conflicts, which cannot
		// happen with a fresh noop meter.
		panic(err)
	}
	return inst
}
