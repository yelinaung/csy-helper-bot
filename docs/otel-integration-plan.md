# OpenTelemetry Integration Plan (v1)

## Overview

Add end-to-end observability to `csy-helper-bot` by integrating
OpenTelemetry (OTel) for **traces, metrics, and logs**, exporting via OTLP
to a local collector (Clickstack / HyperDX). Instrumentation covers every
registered Telegram handler and every external API call (Finnhub, Databento,
LeetCode, Exa, Parallel, Gemini, Telegram photo download).

> **Security-first note (P0):** the existing code embeds credentials in URLs
> — Finnhub puts `token=<API_KEY>` in the query string (6 call sites) and the
> Telegram photo download URL puts `bot<TOKEN>` in the path
> (`internal/bot/ask.go:554`). Because `otelhttp` records `url.full`, a
> sanitizing span exporter is mandatory before any HTTP transport is wrapped.
> See [Credential Sanitization](#credential-sanitization-p0).

### Quick Navigation

- [Goals & Non-Goals](#goals--non-goals) — what v1 does and does not do
- [Current State](#current-state) — what exists today, incl. credential-in-URL map
- [Architecture](#architecture) — package layout, data flow, lifecycle
- [Dependencies](#dependencies) — new modules added to `go.mod`
- [Configuration](#configuration) — env vars (defaults, opt-in)
- [Resource Attributes](#resource-attributes) — service identity
- [New Package: `internal/otel`](#new-package-internalotel) — Providers, exporters, sanitizer, zerolog bridge
- [Credential Sanitization (P0)](#credential-sanitization-p0) — URL redaction before export
- [Instrumentation](#instrumentation) — handlers, HTTP, Gemini, metrics, logs, outcome recorder
- [Span & Metric Catalog](#span--metric-catalog) — names, attributes, units
- [Graceful Shutdown](#graceful-shutdown) — flush order on SIGINT
- [Test Plan](#test-plan) — unit + in-memory exporter tests, isolation strategy
- [Implementation Order](#implementation-order) — 6-step build sequence
- [File Size Estimates](#file-size-estimates) — rough LOC
- [Design Decisions Log](#design-decisions-log) — tradeoffs
- [Future Roadmap](#future-roadmap) — v2 and beyond

> **Non-normative notice:** Sections with concrete signatures, line counts, and
> Go snippets are design guidance that may drift from the final code.
> Authoritative references will live in:
> - `internal/otel/` — providers, exporters, sanitizer, zerolog bridge
> - `internal/bot/bot.go` — handler registration, middleware, outcome recorder
> - `cmd/csy-helper-bot/main.go` — logger wiring, shutdown orchestration

## Goals & Non-Goals

### Goals

1. **Traces** — one span per registered handler; one child span per external
   call (HTTP and Gemini), with low-cardinality names and rich attributes.
2. **Metrics** — command counter + duration histogram, rate-limit counter,
   Gemini token usage (semconv-conformant histogram), plus standard HTTP
   client metrics from `otelhttp`.
3. **Logs** — bridge existing zerolog output into the OTel logs pipeline so
   structured logs appear alongside traces in HyperDX/Clickstack.
4. **OTLP export** — OTLP/HTTP to a configurable endpoint, defaulting to the
   standard local collector port `http://localhost:4318`.
5. **Zero-disruption opt-in** — disabled by default; setting
   `OTEL_ENABLED=true` activates export. CI and tests are unaffected.
6. **Graceful shutdown** — buffered traces/metrics/logs are flushed on SIGINT
   before the process exits.
7. **Credential safety (P0)** — no API key, bot token, or other secret is
   ever exported in a span/log attribute. A sanitizing span exporter strips
   credentials from `url.full` before any trace leaves the process.

### Non-Goals (v1)

- **Trace ↔ log correlation** — requires injecting `trace_id`/`span_id` into
  zerolog fields *before* serialization (the `io.Writer` bridge receives bytes,
  not `context.Context`, so it cannot read the active span). Noted as v2.
- **Telegram Bot API call spans** — `go-telegram/bot` owns its own HTTP client
  which is not easily wrapped. Handler spans already capture user-facing
  latency; Telegram SDK spans are a v2 enhancement.
- **Distributed tracing context propagation across services** — the bot calls
  third-party APIs that do not accept traceparent headers; we only propagate
  context internally (handler → fetcher → HTTP client).
- **Custom dashboards / alerts** — out of scope; the collector UI provides
  exploration out of the box.
- **Profiling / exemplars** — not in v1.
- **Handler error returns** — handlers swallow errors today; a reliable
  `result=error` metric dimension is deferred to v4 (see
  [Outcome Recording](#outcome-recorder) for the v1 `unknown` default).

## Current State

- **Logging**: `rs/zerolog` via a package-global `log.Logger` configured in
  `cmd/csy-helper-bot/main.go` with a `ConsoleWriter` (human-readable, JSON
  internally). No structured telemetry export.
- **Handlers** registered in `internal/bot/bot.go:Run`:
  - `startHandler` (`/start`), `helpHandler` (`/help`), `lcHandler` (`/lc`, `!lc`)
  - `stockHandler` (`!s`, `!s `), `stockAnalysisHandler` (`!sa`, `!sa `)
  - `askHandler` (mention match), `photoAskHandler` (photo + mention)
  - `xLinkHandler` (x.com/twitter.com link rewrite)
  - Default handler for unmatched updates
  - All registered handlers are wrapped by `requestLoggingMiddleware`
    (`internal/bot/bot.go:171`), which logs the incoming update and enforces
    the group allowlist before dispatching.
- **External calls** (instrumentation targets) with **credential location**:
  | Service | Function | Transport | Client | Secret in URL? |
  |---|---|---|---|---|
  | Finnhub quote | `fetchStockQuote` (`stock.go:339`) | HTTP GET | `httpClient` (10s) | **Yes** — `token=` query |
  | Finnhub profile | `fetchCompanyProfile` (`stock.go:381`) | HTTP GET | `httpClient` | **Yes** — `token=` query |
  | Finnhub metrics | `fetchFinancialMetrics` (`stock_fundamentals.go:103`) | HTTP GET | `httpClient` | **Yes** — `token=` query |
  | Finnhub earnings | `fetchEarningsHistory` (`stock_fundamentals.go:143`) | HTTP GET | `httpClient` | **Yes** — `token=` query |
  | Finnhub recommendation | `fetchRecommendation` (`stock_fundamentals.go:184`) | HTTP GET | `httpClient` | **Yes** — `token=` query |
  | Finnhub price-target | `fetchPriceTarget` (`stock_fundamentals.go:228`) | HTTP GET | `httpClient` | **Yes** — `token=` query |
  | Databento | `getHistoricalRangeWithContext` (`stock.go:537`) | HTTP POST | `histHTTPClient` (30s) | No — Basic auth header |
  | LeetCode | `fetchDailyLeetCode` (`leetcode.go:66`) | HTTP POST (GraphQL) | `httpClient` | No — public endpoint |
  | Exa | `searchStockNews` (`exa_search.go:68`) | HTTP POST | `httpClient` | No — `x-api-key` header |
  | Parallel | `parallelSearcher.search` (`parallel_search.go:88`) | HTTP POST | `parallelHTTPClient` | No — `x-api-key` header |
  | Telegram photo | `downloadTelegramPhoto` (`ask.go:545`) | HTTP GET | `httpClient` | **Yes** — `bot<TOKEN>` in path |
  | Gemini explain/ask | `geminiExplainer` `doExplain` (`gemini_explainer.go:336`) | genai SDK | `generator.GenerateContent` | No — SDK-managed auth |
  | Gemini classify | `classifySearchNeed` (`freshness_classifier.go:42`) | genai SDK | `generator.GenerateContent` | No — SDK-managed auth |
  | Gemini analyze | `stockAnalyzer.analyze` (`stock_analysis.go`) | genai SDK | `generator.GenerateContent` | No — SDK-managed auth |

  **7 of the 11 HTTP call sites put credentials in the URL** (6 Finnhub +
  1 Telegram). These are the P0 sanitization targets.
- **Lifecycle**: `Run` builds a `signal.NotifyContext` for SIGINT and blocks on
  `b.Start(ctx)`. `main` calls `log.Fatal` on the returned error.

## Architecture

```text
main.go
  │
  ├─ otel.Setup(ctx, buildInfo) ───────────────┐
  │     │                                      │
  │     ├─ resource (service name/version)     │
  │     ├─ OTLP/HTTP exporters (or stdout)     │  internal/otel
  │     ├─ sanitizing span exporter wrapper    │  (P0: strips url.full + http.url creds)
│     ├─ zerolog bridge w/ URL redaction     │  (P0: same redaction for logs)
  │     ├─ Providers{Tracer,Meter,Logger}      │
  │     ├─ Instruments (domain metrics)        │
  │     ├─ zerolog → OTel log writer           │
  │     ├─ install globals (otel.Set*Provider) │
  │     └─ returns (shutdown, logWriter)       │
  │
  ├─ zerolog.New(io.MultiWriter(console, logWriter))
  ├─ defer shutdown()  (sync.Once, flush on exit)
  │
  └─ appbot.Run()
        │
        ├─ requestLoggingMiddleware  (existing: log + access check)
        │   ↑ composed with
        ├─ tracingMiddleware         (NEW: open handler span, outcome recorder)
        │
        └─ handler ──┬─ fetchXXX (manual span: finnhub.quote) ── otelhttp child span (GET ...)
                     │                       (child's url.full/http.url sanitized at export)
                     ├─ recordOutcome(ctx, "rate_limited")   ← sets result attr (mutex-guarded)
                     ├─ generator.GenerateContent (manual span: gemini.explain)
                     └─ metrics: command duration recorded at span end
```

### Package Layout

A new `internal/otel` package owns telemetry setup. It matches the existing
`internal/bot` layout and the "one concern per package" guideline: telemetry
is a single, independently-testable domain with no knowledge of bot logic.

```text
internal/otel/
├── setup.go           — Setup() production entry; env parsing; resource; global install
├── providers.go       — Providers struct + NewProviders (exporter-injected, test-friendly)
├── traces.go          — tracer provider builder + OTLP/HTTP trace exporter
├── metrics.go         — meter provider builder + OTLP/HTTP metric exporter + Instruments
├── logs.go            — logger provider builder + OTLP/HTTP log exporter
├── sanitizer.go       — sanitizing SpanExporter: strips credentials from url.full (P0)
├── zerolog_bridge.go   — io.Writer that parses zerolog JSON → OTel log records
├── http.go            — NewHTTPTransport(base) wrapper around otelhttp
├── outcome.go         — outcome recorder helpers used by internal/bot middleware
└── *_test.go          — unit tests with in-memory exporters (no global contamination)
```

`internal/bot` imports `internal/otel` for the HTTP transport wrapper, the
instruments, and the outcome recorder helpers. `internal/otel` never imports
`internal/bot` — no cycle.

### Data Flow

```text
Telegram update
  → go-telegram/bot dispatch
  → tracingMiddleware        opens span "bot.command", injects outcomeRecorder into ctx
      → requestLoggingMiddleware (log incoming + access check)
          → handler function
              ├─ on rate-limit: recordOutcome(ctx, "rate_limited"); return
              ├─ on not-configured: recordOutcome(ctx, "not_configured"); return
              ├─ fetchStockQuote      span: "finnhub.quote"   child: otelhttp GET
              │                       (child's url.full sanitized at export time)
              ├─ generator.GenerateContent  span: "gemini.explain"
              └─ ...
  → middleware reads recorder.result (default "unknown"), sets span attr +
    records bot.commands.total{result=...} + bot.command.duration{result=...}
  → on error path: span status = ERROR (best-effort; see Non-Goals)
```

## Dependencies

Added to `go.mod` (pinned by `go mod tidy`; versions are illustrative — use the
latest stable at implementation time):

| Module | Purpose |
|---|---|
| `go.opentelemetry.io/otel` | Core API: `Tracer`, `Meter`, `Logger` globals |
| `go.opentelemetry.io/otel/sdk` | SDK core |
| `go.opentelemetry.io/otel/sdk/trace` | Tracer provider + batch span processor |
| `go.opentelemetry.io/otel/sdk/metric` | Meter provider + periodic reader |
| `go.opentelemetry.io/otel/sdk/log` | Logger provider + batch processor |
| `go.opentelemetry.io/otel/metric` | Metric API instruments |
| `go.opentelemetry.io/otel/log` | Log API records |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace` + `…/otlptracehttp` | OTLP/HTTP trace exporter |
| `go.opentelemetry.io/otel/exporters/otlp/otlpmetric` + `…/otlpmetrichttp` | OTLP/HTTP metric exporter |
| `go.opentelemetry.io/otel/exporters/otlp/otlplog` + `…/otlploghttp` | OTLP/HTTP log exporter |
| `go.opentelemetry.io/otel/exporters/stdout/stdouttrace` (+ metric/log) | Debug stdout fallback |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | HTTP client auto-instrumentation |
| `go.opentelemetry.io/otel/sdk/trace/tracetest` | In-memory span exporter for tests |
| `go.opentelemetry.io/otel/semconv/v1.30.0` | Semantic conventions (see version note below) |

The genai SDK and `go-telegram/bot` are **not** modified; we instrument at our
call boundaries (manual spans around `GenerateContent`, `otelhttp` around our
own `http.Client` transports).

> **Semconv version pinning (P2):** Pin one `semconv` import version across the
> project (currently `v1.30.0` is the latest stable at writing). The
> `otelhttp` contrib package emits HTTP attributes per **its own** pinned
> semconv version, which may differ from the project's import — this is normal
> (contrib chooses the semconv version it was built against). Verify at
> implementation time that the contrib version's emitted attribute names
> (`url.full`, `http.request.method`, `server.address`,
> `http.response.status_code`) match what the target collector expects; if the
> contrib is on a newer semconv with renamed attributes, align the project
> import to the same version. GenAI attributes (`gen_ai.*`) may not yet be
> generated as constants in `semconv/v1.30.0`; if not, define them as string
> constants in `internal/otel` matching the [GenAI semconv spec][genai-semconv].
>
> [genai-semconv]: https://opentelemetry.io/docs/specs/semconv/gen-ai/

## Configuration

All OTel config is env-driven. The standard `OTEL_*` env vars are honored by
the SDK directly (resource attributes, exporter endpoint, headers, batch
sizes). One project-specific switch is added:

| Env var | Default | Purpose |
|---|---|---|
| `OTEL_ENABLED` | `false` | Master switch. `true` enables OTLP export; unset/false installs noop providers (CI/test-safe). |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` | OTLP/HTTP base endpoint. SDK appends `/v1/{traces,metrics,logs}`. |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | (inherits above) | Per-signal trace endpoint override. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | (inherits above) | Per-signal metric endpoint override. |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | (inherits above) | Per-signal log endpoint override. |
| `OTEL_EXPORTER_OTLP_HEADERS` | (none) | e.g. `authorization=Bearer …` for hosted collectors. |
| `OTEL_SERVICE_NAME` | `csy-helper-bot` | Resource `service.name`. |
| `OTEL_RESOURCE_ATTRIBUTES` | (none) | Generic resource attrs, e.g. `deployment.environment=local`. |
| `OTEL_LOGS_ENABLED` | inherits `OTEL_ENABLED` | Set `false` to disable log export while keeping traces/metrics. |
| `OTEL_METRICS_ENABLED` | inherits `OTEL_ENABLED` | Set `false` to disable metrics. |
| `OTEL_TRACES_ENABLED` | inherits `OTEL_ENABLED` | Set `false` to disable traces. |
| `OTEL_EXPORTER=stdout` | (none) | If set to `stdout`, use stdout exporters instead of OTLP (local debugging). |

Clickstack/HyperDX local installs listen on OTLP/HTTP at `:4318` by default, so
the only required setting for local use is `OTEL_ENABLED=true`.

**Credential sanitization is always on** when traces are enabled — there is no
toggle to disable it. See [Credential Sanitization (P0)](#credential-sanitization-p0).

## Resource Attributes

Built once in `internal/otel` and shared by all three providers:

| Attribute | Source |
|---|---|
| `service.name` | `OTEL_SERVICE_NAME` env, default `csy-helper-bot` |
| `service.version` | `main.commit` ldflags value (`unknown` if unset) |
| `build.date` | `main.buildDate` ldflags value |
| `deployment.environment` | `OTEL_RESOURCE_ATTRIBUTES` or `ENVIRONMENT` env |
| `host.name` | detected via `os.Hostname()` (best-effort) |
| `process.pid` | `os.Getpid()` |

The two ldflags variables are passed from `internal/otel` via a `Setup`
parameter (a small `BuildInfo{Commit, Date string}` struct) so `internal/otel`
does not import `main`.

## New Package: `internal/otel`

### `providers.go` — provider-injected construction (test isolation, P1)

To avoid global-provider contamination between tests, providers and instruments
are built by an exporter-injected constructor. `Setup` is the only function
that touches the process globals.

```go
// Providers bundles the three OTel providers, the domain instruments, and the
// zerolog log writer. Tests construct this directly with in-memory exporters
// and never touch the global providers. Setup installs one of these into the
// globals for production use.
type Providers struct {
    TracerProvider *sdktrace.TracerProvider
    MeterProvider  *sdkmetric.MeterProvider
    LoggerProvider *sdklog.LoggerProvider
    Instruments    *Instruments
    LogWriter      io.Writer // io.Discard when logs disabled
}

// Exporters holds the three signal exporters. Injected so tests pass
// in-memory exporters (tracetest.InMemoryExporter, etc.) and production
// passes OTLP/HTTP exporters.
type Exporters struct {
    Traces  sdktrace.SpanExporter
    Metrics sdkmetric.Exporter
    Logs    sdklog.Exporter
}

// NewProviders builds Providers from cfg + res + injected exporters.
// Instruments are constructed from the MeterProvider here, so they are
// always bound to the correct (test or production) provider.
func NewProviders(cfg config, res *resource.Resource, exp Exporters) (*Providers, error)
```

`Instruments` is a struct of cached metric handles (the counters/histograms
listed in [Metrics instruments](#metrics-instruments)), built once via
`meter.Int64Counter(...)`, `meter.Float64Histogram(...)`, etc.

### `setup.go` — production entry point

```go
// BuildInfo carries ldflags-derived build metadata for the resource.
type BuildInfo struct {
    Commit string
    Date   string
}

// Setup is the production entry point: parses env, builds exporters (OTLP/HTTP
// or stdout), wraps the trace exporter in the sanitizer, calls NewProviders,
// installs the providers into the OTel globals (otel.SetTracerProvider, etc.),
// and returns a shutdown func + the zerolog log writer. When OTEL_ENABLED is
// not "true", it installs noop providers and returns io.Discard, so callers
// always get valid values.
func Setup(ctx context.Context, info BuildInfo) (shutdown func() error, logWriter io.Writer, err error)
```

- Parses env once into a `config` struct.
- Builds the resource via `resource.New(ctx, resource.FromAttributes(...))`.
- Builds exporters (OTLP/HTTP or stdout), **wraps the trace exporter in
  `newSanitizingExporter`** before passing it to `NewProviders`.
- Installs globals via `otel.SetTracerProvider`, `otel.SetMeterProvider`, and
  the package-local logger provider used by the bridge.
- `shutdown` is guarded by `sync.Once` and a 5s timeout; it calls each
  provider's `Shutdown` in order (traces → metrics → logs).

> **Test isolation (P1):** `Setup` is the *only* function that mutates the
> process-global providers. All other `internal/otel` tests construct
> `*Providers` via `NewProviders` with in-memory exporters and assert on the
> returned struct directly — they never call `otel.SetTracerProvider`, so
> parallel tests cannot contaminate each other. The single global-install path
> in `Setup` is verified by **one non-parallel in-process test**
> (`TestSetup_InstallsGlobals`) that calls `Setup` with a stubbed exporter,
> asserts the globals are set, and restores the previous providers in
> `t.Cleanup`. OTel's global setters are idempotent and the delegating global
> picks up the latest provider, so a subprocess is not required; keeping the
> test in-process avoids `exec.Command` complexity (review #6). The test does
> **not** call `t.Parallel()` to avoid racing with any other test that might
> touch the globals. See [Test Plan](#test-plan).

### `traces.go`

```go
func newTracerProvider(res *resource.Resource, exp sdktrace.SpanExporter, cfg config) *sdktrace.TracerProvider
```

- Batch span processor wrapping the (already-sanitized) exporter.
- Sets `otel.SetTracerProvider(tp)` — called only from `Setup`.

### `metrics.go`

```go
func newMeterProvider(res *resource.Resource, exp sdkmetric.Exporter, cfg config) *sdkmetric.MeterProvider
```

- Periodic reader with a 30s default export interval (`OTEL_METRIC_EXPORT_INTERVAL`).
- `Instruments` struct built from the meter in `NewProviders`.

### `logs.go`

```go
func newLoggerProvider(res *resource.Resource, exp sdklog.Exporter, cfg config) *sdklog.LoggerProvider
```

- Batch log processor.
- A package-level `loggerProvider` is set only by `Setup`; the bridge reads it
  via `otelLogger()`, which falls back to a noop logger when unset (test-safe).

### `sanitizer.go` — credential sanitizing exporter (P0)

```go
// urlAttrKeys is the set of span/log attribute keys whose values may carry a
// URL with embedded credentials. Both names are redacted to stay robust to
// semconv version drift: newer otelhttp emits url.full, older versions emit
// http.url. Redacting only one would silently leak the token if the pinned
// contrib version uses the other name — the worst failure mode for a
// fail-closed safety net.
var urlAttrKeys = map[string]struct{}{
    "url.full": {},
    "http.url": {},
}

// newSanitizingExporter wraps a SpanExporter and strips credentials from any
// URL-bearing attribute (url.full or http.url) before export. Guards against
// Finnhub query tokens (token=<API_KEY>) and Telegram bot-token path segments
// (bot<TOKEN>) leaking into trace backends. Always applied to the trace
// exporter in Setup when traces are enabled.
func newSanitizingExporter(base sdktrace.SpanExporter) sdktrace.SpanExporter
```

- `Export(ctx, spans)` iterates the spans, and for each span rebuilds the
  attribute list redacting **every key in `urlAttrKeys`** (both `url.full` and
  the legacy `http.url`):
  - Parses the URL; if a query parameter named `token`, `api_key`, `apikey`,
    or `key` is present, replaces its value with `<redacted>`.
  - If the path contains a segment matching `^bot\d+:[A-Za-z0-9_-]+$`
    (Telegram bot token format), replaces that segment with `bot<redacted>`.
  - If parsing fails, replaces the entire URL value with `<redacted>`
    (fail-closed).
- Non-URL attributes pass through unchanged.
- The span name (set by `otelhttp`'s `WithSpanNameFormatter` to
  `"<METHOD> <HOST>"`) never contains the URL path or query, so no additional
  sanitization is needed there.
- **Test binding (review #2):** `TestSanitizer_*` must assert against the
  **actual attribute key the pinned `otelhttp` contrib version emits**
  (determined at implementation time by a one-off probe test), not an assumed
  key. Add `TestSanitizer_RedactsBothUrlKeys` that feeds spans with both
  `url.full` and `http.url` and confirms both are redacted — so the safety net
  holds regardless of which name the contrib uses.
- This is a **defense-in-depth** safety net: even if a future code path adds a
  secret to a URL, or the contrib is upgraded and switches attribute names, no
  credential reaches the collector. The recommended long-term fix (migrating
  Finnhub to header auth) is tracked in the roadmap.

### `zerolog_bridge.go`

The OTel contrib project ships no stable zerolog bridge as of writing, so we
write a small writer-based one:

```go
// otelLogWriter is an io.Writer that parses each zerolog JSON line and
// emits an OTel log record. It is safe for concurrent use.
type otelLogWriter struct {
    logger log.Logger
}

func (w *otelLogWriter) Write(p []byte) (int, error)
```

- zerolog always emits one JSON object per line followed by `\n`; the writer
  splits on newlines and decodes each line with `encoding/json`.
- Field mapping:
  | zerolog JSON key | OTel log record field |
  |---|---|
  | `time` | `Record.Timestamp` |
  | `level` | `Record.Severity` (map: trace→TRACE, debug→DEBUG, info→INFO, warn→WARN, error→ERROR, fatal→FATAL, panic→FATAL) |
  | `message` | `Record.Body` (StringValue) |
  | every other key | `Record.Attributes` (best-effort type inference: number→Float64, bool→Bool, string→String, object/array→String of raw JSON) |
- `Record.ObservedTimestamp` is set to `time.Now()` to preserve export order.
- **Log-side URL redaction (P0, review #3):** before attaching any attribute
  whose key is `url`, `url.full`, `http.url`, or `request.url`, the bridge
  calls the same `redactURL(string) string` used by the span sanitizer. This
  guards against a future `log.Error().Str("url", req.URL.String())` shipping
  a Finnhub token or Telegram bot token via the log pipeline, since the span
  sanitizer only covers spans. No existing log site logs a
  credential-bearing URL (audited — see [Credential Sanitization](#credential-sanitization-p0));
  this is defense-in-depth.
- **Trace correlation is not possible from the writer alone (P2):**
  `io.Writer.Write` receives bytes, not `context.Context`, so the bridge
  cannot read the active span context. Correlation requires injecting
  `trace_id`/`span_id` into the zerolog JSON *before* serialization — via
  context-aware logging or a zerolog hook — and then the writer can read those
  fields from the JSON. This is v2 work; see [Future Roadmap](#future-roadmap).

`Setup` returns an `io.Discard` writer when logs are disabled, so `main.go`
always attaches a valid writer to the `MultiWriter`.

### `http.go`

```go
// NewHTTPTransport wraps base (or http.DefaultTransport when nil) with
// otelhttp, producing standard HTTP client spans and metrics. The span name
// is formatted as "<METHOD> <HOST>" (never the full URL) to avoid leaking
// credentials that some endpoints put in the path/query. Safe to call before
// the SDK is initialized: with a noop tracer/meter provider, otelhttp
// produces no-op spans and skips metric recording.
func NewHTTPTransport(base http.RoundTripper) http.RoundTripper
```

- Options: `otelhttp.WithSpanNameFormatter(func(req *http.Request) string {
  return fmt.Sprintf("%s %s", req.Method, req.URL.Host) })`.
- The `url.full` (and legacy `http.url`) attribute that `otelhttp` adds is
  sanitized at export time by `sanitizer.go` — this is the second layer of
  defense after the safe span name. The zerolog bridge applies the same
  redaction to log records (third layer — see [Credential Sanitization](#credential-sanitization-p0)).

> **Transport rewire timing & double-wrap guard (review #5):** The package-level
> `httpClient`, `histHTTPClient`, and `parallelHTTPClient` are rewired in an
> explicit `init()` in `internal/bot` that wraps their *existing* transports.
> The wrapper must wrap the **original** transport, not a transport that has
> already been otel-wrapped (double-wrapping produces duplicate nested spans
> and double-counted metrics). Because the test redirection pattern
> (`bot_test.go:62`) replaces the entire `*http.Client` variable wholesale, the
> wrapped transport is bypassed in tests — no race, no double-wrap in tests.
> The `init()` runs before any test, and tests overwrite the var, so ordering
> is safe.

### `outcome.go` — handler outcome recorder (P1)

A context-carried recorder lets handlers communicate their outcome to the
outer tracing middleware **without changing handler signatures**:

```go
type outcomeKey struct{}

type outcomeRecorder struct {
    mu     sync.Mutex // guards result against handlers that spawn goroutines
    result string     // default "unknown"
}

// withOutcomeRecorder returns a context carrying a fresh recorder (default
// result "unknown") and the recorder itself for the middleware to read.
func withOutcomeRecorder(ctx context.Context) (context.Context, *outcomeRecorder)

// recordOutcome sets the result on the recorder in the context, if present.
// No-op when no recorder is in the context (e.g. called outside a handler).
// Safe for concurrent use: a handler that does `go func(){ recordOutcome(ctx,
// ...) }()` races with the middleware's read unless the result is guarded.
// The mutex keeps mise run test-race clean.
func recordOutcome(ctx context.Context, result string)

// Result returns the current outcome under the mutex.
func (r *outcomeRecorder) Result() string
```

Handlers call `recordOutcome(ctx, "rate_limited")`, `recordOutcome(ctx,
"not_configured")`, `recordOutcome(ctx, "blocked")`, etc. at their known
decision points. The middleware reads `recorder.result` at span end and sets
the `bot.result` span attribute + the `bot.commands.total` /
`bot.command.duration` metric attribute. **The default is `unknown`, not
`success`** — so dashboards never show a false green when the outcome was not
positively confirmed. See [Design Decisions #6](#design-decisions-log).

## Credential Sanitization (P0)

**Problem:** `otelhttp` records the request URL as a span attribute. Two
services embed secrets in the URL:

1. **Finnhub** (6 call sites) — `https://finnhub.io/api/v1/quote?symbol=AAPL&token=<API_KEY>`.
   All six fetchers in `stock.go` / `stock_fundamentals.go` use `q.Set("token", apiKey)`.
2. **Telegram photo download** (`ask.go:554`) — `b.FileDownloadLink(file)` returns
   `https://api.telegram.org/file/bot<TOKEN>/<path>`. The bot token is in the URL path.

Without sanitization, exporting the URL attribute would ship the Finnhub API
key and the Telegram bot token to the collector — a credential leak.

**Mitigation (v1, defense-in-depth, three layers):**

1. **Safe span names** — `otelhttp.WithSpanNameFormatter` produces
   `"<METHOD> <HOST>"` (e.g. `GET finnhub.io`), never including path or query.
2. **Sanitizing span exporter** (`sanitizer.go`) — wraps the OTLP trace
   exporter and rewrites **both `url.full` and the legacy `http.url`**
   attribute before export: redacts `token`/`api_key`/`apikey`/`key` query
   values and `bot<TOKEN>` path segments. Fail-closed: unparseable URLs become
   `<redacted>`. Applied unconditionally in `Setup` when traces are enabled.
   Redacting both keys keeps the safety net intact regardless of which semconv
   version the pinned `otelhttp` contrib emits (see review #2).
3. **Log-side redaction in the zerolog bridge** (`zerolog_bridge.go`) — the
   bridge applies the same URL redaction to any log attribute whose key is
   `url`, `url.full`, `http.url`, or `request.url` (zerolog field names are
   caller-chosen, so cover the common ones). This guards against a future
   `log.Error().Str("url", req.URL.String())` call shipping a token via the
   log pipeline, since `newSanitizingExporter` only covers spans. **Current
   audit:** no existing `log.*` call site in `internal/bot` logs a
   credential-bearing URL (the only URL-logging site is `bot.go:136`
   `Str("path", r.URL.Path)` on the `/health` endpoint, which carries no
   secret). The bridge redaction is defense-in-depth, not a fix for an
   existing leak.
4. **No secret in other span attributes** — manual parent spans (`finnhub.quote`,
   etc.) set only `finnhub.endpoint`, `symbol`, `databento.dataset` — never the
   URL or token.

**Why logs get their own redaction (review #3):** `newSanitizingExporter` is a
`SpanExporter` — it never sees log records. The whole point of P0 is "no
secret ever reaches the collector," and logs flow to the same collector via a
separate pipeline. A single `log.Error().Str("url", …)` in a future error path
would bypass the span sanitizer entirely. The bridge-level redaction closes
that gap at the cost of one extra `redactURL(string) string` call per log
attribute.

**Long-term fix (roadmap, independent of OTel):** migrate Finnhub to
header-based auth (`X-Finnhub-Token` or `Authorization: Bearer <key>`) if the
API supports it, removing the token from the URL entirely. The Telegram bot
token in the photo-download path cannot be avoided (Telegram's API requires
it), so the sanitizer remains the permanent guard for that call.

## Instrumentation

### Handler tracing middleware

A new `tracingMiddleware` in `internal/bot/bot.go` composes with the existing
`requestLoggingMiddleware`. It wraps **outermost** so the span covers the
access check and the handler:

```go
func tracingMiddleware(name string, next bot.HandlerFunc) bot.HandlerFunc {
    return func(ctx context.Context, b *bot.Bot, update *models.Update) {
        ctx, span := tracer.Start(ctx, name)
        defer span.End()

        ctx, recorder := otel.WithOutcomeRecorder(ctx)
        start := time.Now()
        next(ctx, b, update)
        elapsed := time.Since(start)

        result := recorder.Result() // "unknown" unless handler called recordOutcome
        span.SetAttributes(attribute.String("bot.result", result))
        otel.Instruments().CommandsTotal.Add(ctx, 1,
            metric.WithAttributes(
                attribute.String("bot.command", name),
                attribute.String("bot.result", result),
            ))
        otel.Instruments().CommandDuration.Record(ctx, elapsed.Seconds(),
            metric.WithAttributes(
                attribute.String("bot.command", name),
                attribute.String("bot.result", result),
            ))
    }
}
```

- Span name = a fixed operation name per handler (see [Span Catalog](#spans));
  the literal command text (`/lc` vs `!lc`) is preserved in the `bot.command`
  attribute for filtering.
- Attributes: `bot.command` (operation name), `bot.command.literal` (the exact
  text the user typed, e.g. `/lc` or `!lc`), `bot.chat_id`, `bot.chat_type`,
  `bot.user_id`, `bot.update_id`, `bot.result`.
- Span status: set to `ERROR` only when the handler records an error outcome
  (v4 will make this reliable via handler error returns). In v1, `result=error`
  is rarely set — see Decision #6.
- The default handler is wrapped with `tracingMiddleware("bot.unmatched", ...)`.
- Registration sites in `Run` change from
  `b.RegisterHandler(..., startHandler, requestLoggingMiddleware)` to
  `b.RegisterHandler(..., obs("/start", startHandler))` where
  `obs(name, h) = tracingMiddleware(name, requestLoggingMiddleware(h))`.
- Both `/lc` and `!lc` registrations map to the same span name `bot.lc`; the
  `bot.command.literal` attribute distinguishes them. Same for `!s`/`!s ` →
  `bot.stock` and `!sa`/`!sa ` → `bot.stock_analysis`.

### HTTP client instrumentation

An explicit `init()` in `internal/bot` rewires the three package-level HTTP
clients (`httpClient`, `histHTTPClient`, `parallelHTTPClient`) to use
`otel.NewHTTPTransport` around their existing transports, preserving the
10s/30s/no-timeout client timeouts. See the [double-wrap guard](#httpgo) note.

Each fetch function gains a **manual parent span** so the trace UI shows a
domain-level operation above the automatic HTTP client span:

```go
func fetchStockQuote(ctx context.Context, symbol string) (*StockQuote, error) {
    ctx, span := tracer.Start(ctx, "finnhub.quote",
        trace.WithAttributes(
            attribute.String("finnhub.endpoint", "quote"),
            attribute.String("symbol", symbol),
        ),
    )
    defer span.End()
    // existing body unchanged
}
```

All 11 HTTP fetch sites and the 3 Gemini call sites get manual spans.
The `downloadTelegramPhoto` call gets a `telegram.download_photo` parent span
(the HTTP GET is a child; its `url.full` is sanitized at export).

### Gemini instrumentation (manual spans, semconv-conformant)

The genai SDK does not expose an HTTP transport we can wrap, so each
`GenerateContent` call site gets a manual span with GenAI semantic
conventions:

```go
ctx, span := tracer.Start(ctx, "gemini.explain",
    trace.WithAttributes(
        attribute.String("gen_ai.provider.name", "gemini"),
        attribute.String("gen_ai.operation.name", "generate_content"),
        attribute.String("gen_ai.request.model", model),
    ),
)
defer span.End()
resp, err := g.generator.GenerateContent(ctx, model, contents, config)
// on success: record token usage histogram from resp.UsageMetadata
```

> **Semconv note (P2):** Use `gen_ai.provider.name` (current convention); the
> older `gen_ai.system` is deprecated in newer semconv. If the pinned
> `semconv/v1.30.0` package does not generate `gen_ai.*` constants, define
> string constants in `internal/otel` matching the [GenAI semconv spec].
> `gen_ai.operation.name` uses the semconv-accurate value **`generate_content`**
> for all three call sites (all of them call `GenerateContent`;
> `"chat"`/`"classify"` are not standard enum values — review #5). If the
> spec at implementation time defines a more specific value for a
> classification-style call, use that; otherwise `generate_content` is the
> safe default. Distinguish the three sites via the span name
> (`gemini.explain` / `gemini.classify` / `gemini.analyze`) and a custom
> `gen_ai.request.operation` attribute if finer granularity is needed.

Three call sites:
- `doExplain` (`gemini_explainer.go:336`) — `gemini.explain`
- `classifySearchNeed` (`freshness_classifier.go:42`) — `gemini.classify`
- `stockAnalyzer.analyze` (`stock_analysis.go`) — `gemini.analyze`

### Metrics instruments

The `Instruments` struct, built from the `MeterProvider` in `NewProviders`,
defines these once in `internal/otel`:

| Instrument | Type | Unit | Attributes | Recorded where |
|---|---|---|---|---|
| `bot.commands.total` | Counter | `1` | `bot.command`, `bot.result` (success/unknown/rate_limited/not_configured/blocked/error), `bot.chat_type` | `tracingMiddleware` end |
| `bot.command.duration` | Histogram | `s` | `bot.command`, `bot.result` | `tracingMiddleware` end |
| `bot.rate_limited.total` | Counter | `1` | `feature` (explain/analysis) | `allowExplainRequest`, `allowAnalysisRequest` |
| `gen_ai.client.token.usage` | **Histogram** | `{token}` | `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.token.type` (input/output) | after each `GenerateContent` when `resp.UsageMetadata` is non-nil |
| `http.client.request.duration` | Histogram | `s` | `http.request.method`, `server.address`, `http.response.status_code` | automatically by `otelhttp` |

> **`gen_ai.client.token.usage` is a Histogram, not a Counter (P2, review #3):**
> The OTel GenAI semconv defines this metric name as a histogram. Reusing a
> reserved semconv name with a different instrument type can break backends
> that apply semconv-aware processing (HyperDX/Clickstack may mis-render it).
> A histogram still sums fine for "total spend per model" and gives per-request
> distributions for free. If a pure total is also wanted, add a clearly-custom
> `bot.gen_ai.tokens.total` counter — but the semconv name must stay a
> histogram. See [Design Decisions #17](#design-decisions-log).

> **`bot.result` default is `unknown` (P1, review #4):** Handlers swallow
> errors, so `error` cannot be reliably detected in v1. Emitting
> `result=success` by default would give dashboards a false green. The default
> is `unknown`; only specific outcomes detected via `recordOutcome` set a
> definite value. `success` is set only where positively confirmable in v1
> (e.g. a handler that completes its main path without an early-return
> outcome) — otherwise `unknown`. This makes the metric honestly
> three-valued: known-success, known-failure-cause, and unknown. See
> [Design Decisions #6](#design-decisions-log).

The HTTP client metrics come for free from `otelhttp` and need no manual code.
Domain metrics (commands, rate-limits, tokens) use the cached `Instruments`.

### Zerolog → OTel logs bridge

`main.go` attaches the OTel log writer returned by `Setup` as a second sink
to zerolog:

```go
shutdown, otelLogWriter, err := otel.Setup(ctx, otel.BuildInfo{Commit: commit, Date: buildDate})
log.Logger = zerolog.New(io.MultiWriter(
    zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339},
    otelLogWriter,
)).With().Timestamp().Caller().Logger()
defer shutdown()
```

- The console writer continues to produce human-readable output for local dev.
- The OTel writer receives the same JSON zerolog emits and forwards structured
  records to the collector.
- When `OTEL_LOGS_ENABLED=false` or `OTEL_ENABLED!=true`, `otelLogWriter` is
  `io.Discard` — zero overhead.

## Span & Metric Catalog

### Spans

Handler span names are fixed operation names (low cardinality); the literal
command text is in `bot.command.literal`. Both aliases of a command collapse to
one span name.

| Span name | Aliases that map to it | Kind | Parent of | Key attributes |
|---|---|---|---|---|
| `bot.start` | `/start` | internal | — | `bot.command`, `bot.command.literal`, `bot.chat_id`, `bot.user_id`, `bot.result` |
| `bot.help` | `/help` | internal | — | same |
| `bot.lc` | `/lc`, `!lc` | internal | `leetcode.daily` | same |
| `bot.stock` | `!s`, `!s ` | internal | `finnhub.quote`/`finnhub.profile`/`databento.get_range` | same + `symbol` |
| `bot.stock_analysis` | `!sa`, `!sa ` | internal | `finnhub.*` + `exa.search` + `gemini.analyze` | same + `symbol` |
| `bot.ask` | mention match | internal | `gemini.explain`/`gemini.classify`/`parallel.search` | same |
| `bot.photo_ask` | photo + mention | internal | `telegram.download_photo` + `gemini.explain` | same |
| `bot.xlink` | x.com/twitter link | internal | — | same |
| `bot.unmatched` | default handler | internal | — | same |
| `finnhub.quote` / `finnhub.profile` / `finnhub.metrics` / `finnhub.earnings` / `finnhub.recommendation` / `finnhub.price_target` | — | client | otelhttp `GET finnhub.io` | `finnhub.endpoint`, `symbol`; child has `url.full` (sanitized), `http.response.status_code` |
| `databento.get_range` | — | client | otelhttp `POST hist.databento.com` | `symbol`, `databento.dataset`, `databento.schema` |
| `leetcode.daily` | — | client | otelhttp `POST leetcode.com` | `leetcode.operation` |
| `exa.search` | — | client | otelhttp `POST api.exa.ai` | `exa.query`, `exa.num_results` |
| `parallel.search` | — | client | otelhttp `POST api.parallel.ai` | `parallel.objective`, `parallel.queries_count` |
| `telegram.download_photo` | — | client | otelhttp `GET api.telegram.org` | `telegram.file_id` (truncated); child `url.full` sanitized (bot token redacted) |
| `gemini.explain` / `gemini.classify` / `gemini.analyze` | — | client | (none — SDK transport not wrapped) | `gen_ai.provider.name`, `gen_ai.operation.name` (`generate_content`), `gen_ai.request.model`, `gen_ai.response.finish_reason` |

### Metrics

See the [Metrics instruments](#metrics-instruments) table above.

## Graceful Shutdown

`internal/otel.Setup` returns a `shutdown func() error` guarded by
`sync.Once` and an internal 5s timeout. The shutdown order is:

1. `TracerProvider.Shutdown` — flushes the batch span processor (and the
   sanitizer forwards to the OTLP exporter).
2. `MeterProvider.Shutdown` — does a final metric export.
3. `LoggerProvider.Shutdown` — flushes the batch log processor.
4. Each OTLP exporter is closed by its provider's shutdown.

Wiring:

- `main.go`: `defer shutdown()` runs on normal return.
- On the `log.Fatal` path (`appbot.Run()` returns an error), `main` calls
  `shutdown()` explicitly before `log.Fatal` — `sync.Once` makes the double
  call safe. `log.Fatal` itself calls `os.Exit(1)`, so the explicit call is
  required for the error path to flush.
- `appbot.Run()` owns the SIGINT context; when SIGINT arrives, `b.Start(ctx)`
  returns, `Run` returns `nil`, `main` returns, and `defer shutdown()` fires.
- The health server (`startHealthServer`) is a goroutine with no shutdown
  hook today; it is not on the flush path and is acceptable to leave as-is
  for v1 (noted as a minor cleanup in v2).

## Test Plan

> **Non-normative.** Test names describe intent — actual names may vary.

### Test isolation strategy (P1)

All `internal/otel` unit tests construct `*Providers` via `NewProviders` with
in-memory exporters (`tracetest.InMemoryExporter`, a manual reader for
metrics, `logtest`/a capturing recorder for logs) and assert on the returned
struct — they **never** call `otel.SetTracerProvider` / `SetMeterProvider` /
`SetLoggerProvider`, so parallel tests cannot contaminate each other's
globals or retain instruments bound to a shutdown provider. The single
global-install path inside `Setup` is verified by **one non-parallel
in-process test** (`TestSetup_InstallsGlobals`) that restores the prior
providers in `t.Cleanup` — no subprocess needed (review #6).

### `internal/otel` (new package)

| Test | Description |
|---|---|
| `TestNewProviders_DisabledConfig` | Noop exporters → providers are valid noops, `LogWriter == io.Discard` |
| `TestNewProviders_OTLPExporters` | OTLP exporters injected → providers wired, instruments non-nil |
| `TestNewProviders_ShutdownFlushes` | After `Shutdown`, a second `Shutdown` is a no-op (idempotent) |
| `TestSetup_InstallsGlobals` | **Non-parallel in-process** (review #6): `OTEL_ENABLED=true` with a stubbed exporter → globals set; previous providers restored in `t.Cleanup` |
| `TestSetup_DisabledByDefault` | `OTEL_ENABLED` unset → noop providers, `logWriter == io.Discard` |
| `TestSetup_PerSignalDisable` | `OTEL_ENABLED=true`, `OTEL_LOGS_ENABLED=false` → traces+metrics on, logs noop |
| `TestSetup_ShutdownTimeout` | A hung exporter is abandoned after the internal 5s timeout |
| `TestSanitizer_StripsFinnhubToken` | Span with `url.full=https://finnhub.io/api/v1/quote?symbol=AAPL&token=secret` → exported as `...&token=<redacted>` |
| `TestSanitizer_RedactsTelegramBotToken` | Span with `url.full=https://api.telegram.org/file/bot123:abc-def/file.jpg` → `bot<redacted>` in path |
| `TestSanitizer_RedactsLegacyHttpUrlKey` | Span with `http.url=...token=secret` (legacy semconv) → redacted (review #2: cover both keys) |
| `TestSanitizer_RedactsBothUrlKeys` | Span carrying both `url.full` and `http.url` → both redacted |
| `TestSanitizer_PassesCleanURLs` | URL with no secret params → unchanged |
| `TestSanitizer_FailClosedOnGarbage` | Unparseable `url.full` → `<redacted>` |
| `TestSanitizer_MatchesContribKey` | Probe the pinned `otelhttp` contrib to confirm which URL attribute key it emits; assert the sanitizer redacts that key. Guards against a future contrib upgrade silently switching keys. |
| `TestZerologBridge_LevelMapping` | Table-driven: each zerolog level → correct OTel severity |
| `TestZerologBridge_Attributes` | Numeric, bool, string, nested object fields map to typed attributes |
| `TestZerologBridge_Multiline` | Multiple JSON lines in one `Write` produce one record each |
| `TestZerologBridge_EmptyAndGarbage` | Empty/non-JSON input does not panic; non-JSON is dropped |
| `TestZerologBridge_RedactsURLAttributes` | Log record with `url`/`url.full`/`http.url`/`request.url` field containing `token=secret` → redacted in exported record (review #3) |
| `TestZerologBridge_LevelMapping` | Table-driven: each zerolog level → correct OTel severity |
| `TestZerologBridge_Attributes` | Numeric, bool, string, nested object fields map to typed attributes |
| `TestZerologBridge_Multiline` | Multiple JSON lines in one `Write` produce one record each |
| `TestZerologBridge_EmptyAndGarbage` | Empty/non-JSON input does not panic; non-JSON is dropped |
| `TestNewHTTPTransport_Noop` | With a noop tracer, `NewHTTPTransport` produces no spans |
| `TestNewHTTPTransport_RecordsSpan` | With in-memory exporter, a round trip yields a span named `GET <host>` (no path/query in name) |
| `TestNewHTTPTransport_NoDoubleWrap` | Wrapping a transport twice is prevented or detected (see review #5) |
| `TestOutcomeRecorder_DefaultUnknown` | Fresh recorder reports `unknown` until `recordOutcome` is called |
| `TestOutcomeRecorder_SetResult` | `recordOutcome(ctx, "rate_limited")` updates the recorder in context |
| `TestOutcomeRecorder_ConcurrentSafe` | `recordOutcome` called from a goroutine while the middleware reads `Result()` — no race under `mise run test-race` (review #4) |
| `TestResourceAttributes` | `BuildInfo`, `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` surface on the resource |

### `internal/bot` (existing package, additions)

| Test | Description |
|---|---|
| `TestTracingMiddleware_OpensSpan` | In-memory tracer: dispatching a fake handler yields one span named `bot.<name>` with `bot.command` set |
| `TestTracingMiddleware_DefaultResultUnknown` | Handler that does not call `recordOutcome` → `bot.result=unknown` (never `success`) |
| `TestTracingMiddleware_RecordsCommandMetric` | `bot.commands.total{result=unknown}` increments by 1 |
| `TestTracingMiddleware_PropagatesOutcome` | Handler calls `recordOutcome(ctx, "rate_limited")` → span attr + metric `result=rate_limited` |
| `TestFetchStockQuote_CreatesSpan` | In-memory tracer + redirected HTTP server: `finnhub.quote` span + child HTTP span recorded; child `url.full` is sanitized |
| `TestFetchStockQuote_SanitizesToken` | The exported `url.full` for a Finnhub call does **not** contain the `token` value |
| `TestDownloadTelegramPhoto_SanitizesToken` | The exported `url.full` for the Telegram photo download has `bot<redacted>` |
| `TestGeminiExplain_CreatesSpan` | `gemini.explain` span recorded around a mock generator; token histogram recorded when `UsageMetadata` is set |
| `TestRateLimiter_Metric` | `bot.rate_limited.total{feature=explain}` increments when the limiter denies |

Existing tests must continue to pass unchanged. The test redirection pattern
in `bot_test.go:62` is preserved because tests replace the whole `http.Client`
(bypassing the otelhttp wrapper); with a noop tracer (the default in tests
since `OTEL_ENABLED` is unset), no spans or metrics are produced, so there is
no telemetry noise and no collector dependency.

### Coverage

The actual CI coverage threshold is **40%**
(`.github/workflows/ci.yml:143`, `THRESHOLD=40`) — not 50%. `internal/otel`
targets ≥80% line coverage to keep the project comfortably above the 40% floor.
`internal/bot` coverage should not drop; new instrumentation code is mostly
thin span-start/end calls and `recordOutcome` one-liners.

### Verification commands

Always run via `mise run <task>` — the tasks set `GOCACHE`/`GOMODCACHE` to
`.cache/`, matching CI. Raw `go test` will not match CI because it uses
different cache directories.

```bash
mise run test
mise run test-race
mise run lint
```

> **Integration tests (P1, review #4):** AGENTS.md mandates `mise run
> test-integration` and `mise test-integration 2>&1 | grep -w 'FAIL:'` before
> pushing. However, **`test-integration` is not defined in `mise.toml`** and no
> integration test suite or `TEST_DATABASE_URL` infrastructure exists in this
> repo (it is a Telegram bot with no database). This is a pre-existing
> inconsistency between AGENTS.md and the actual task definitions. Before
> implementation, resolve it: either add a `test-integration` task to
> `mise.toml` (even a no-op alias to `test` for now, or a real integration
> suite if one is planned) or update AGENTS.md to drop the mandate. Once
> resolved, run `mise run test-integration` at every step's verification and
> before pushing, per AGENTS.md.

## Implementation Order

> **Non-normative.** Build sequence guidance; step boundaries may shift.
> Tests are written alongside each step and `mise run lint` is run after each.
> Run `mise run test`, `mise run test-race`, and (once the task exists — see
> the [Integration tests](#verification-commands) note) `mise run
> test-integration` after each step.

### Step 1: `internal/otel` skeleton + traces + sanitizer (P0)

1. `go get` the dependencies listed in [Dependencies](#dependencies).
2. Create `internal/otel/providers.go`, `setup.go`, `traces.go`, `http.go`,
   `sanitizer.go`, `outcome.go`.
3. Implement `Providers` + `NewProviders` (exporter-injected) and the disabled
   noop path.
4. Implement `newSanitizingExporter` + a shared `redactURL(string) string`
   helper. The exporter redacts **both `url.full` and `http.url`** (Finnhub
   `token` query + Telegram `bot<TOKEN>` path, fail-closed). The helper is
   exported for reuse by the zerolog bridge in Step 3.
5. Implement `Setup` (env parse, resource, OTLP/HTTP trace exporter wrapped in
   sanitizer, global install, `sync.Once` shutdown).
6. Implement `NewHTTPTransport` (safe span name `"<METHOD> <HOST>"`) and the
   `init()` double-wrap guard.
7. Implement the outcome recorder (`withOutcomeRecorder`, `recordOutcome`,
   `Result`) with a `sync.Mutex` guarding `result` (review #4).
8. Write `TestNewProviders_*`, `TestSanitizer_*` (incl. dual-key + contrib-key
   probe), `TestNewHTTPTransport_*`, `TestOutcomeRecorder_*` (incl.
   `ConcurrentSafe`), `TestSetup_DisabledByDefault`.
9. Wire `main.go` to call `Setup`, `defer shutdown()`, and attach the log
   writer to the zerolog `MultiWriter`.
10. Run `mise run test`, `mise run test-race`, `mise run lint`.

### Step 2: Metrics + logs providers

1. Add `metrics.go` and `logs.go` with OTLP/HTTP exporters + batch processors.
2. Add per-signal disable flags.
3. Implement the `Instruments` struct (`bot.commands.total`,
   `bot.command.duration`, `bot.rate_limited.total`,
   `gen_ai.client.token.usage` as a **histogram**) in `NewProviders`.
4. Write provider + instrument tests with in-memory exporters.
5. Write `TestSetup_PerSignalDisable`, `TestSetup_InstallsGlobals`
   (**non-parallel in-process**, restore prior providers in `t.Cleanup`).
6. Run `mise run test`, `mise run test-race`, `mise run lint`.

### Step 3: Zerolog → OTel log bridge (with P0 log redaction)

1. Implement `zerolog_bridge.go`. For any attribute whose key is `url`,
   `url.full`, `http.url`, or `request.url`, call the shared `redactURL`
   helper from Step 1 before attaching it to the log record (review #3).
2. Write the level-mapping, attribute, multiline, garbage, and
   `TestZerologBridge_RedactsURLAttributes` tests.
3. Confirm `main.go` attaches the writer; verify locally that logs appear in
   HyperDX/Clickstack with correct severity and that a synthetic
   `log.Error().Str("url", "...token=secret")` is redacted end-to-end.
4. Run `mise run test`, `mise run test-race`, `mise run lint`.

### Step 4: Handler + HTTP instrumentation in `internal/bot`

1. Add `tracingMiddleware` + the `obs(handler, name)` registration helper.
2. Rewire all `b.RegisterHandler*` calls and the default handler; collapse
   `/lc`+`!lc` → `bot.lc`, `!s`+`!s ` → `bot.stock`, `!sa`+`!sa ` →
   `bot.stock_analysis` (literal in `bot.command.literal`).
3. Rewire `httpClient`, `histHTTPClient`, `parallelHTTPClient` transports via
   `otel.NewHTTPTransport` in `init()` (with the double-wrap guard).
4. Add manual parent spans to the 11 HTTP fetch functions and
   `downloadTelegramPhoto`.
5. Record `bot.commands.total` / `bot.command.duration` in the middleware with
   `bot.result` from the outcome recorder (default `unknown`).
6. Wire `recordOutcome(ctx, "rate_limited")` / `"not_configured"` / `"blocked"`
   at the known decision points in `askHandler`, `photoAskHandler`,
   `stockHandler`, `stockAnalysisHandler`.
7. Record `bot.rate_limited.total` in `allowExplainRequest` /
   `allowAnalysisRequest`.
8. Write `TestTracingMiddleware_*`, `TestFetchStockQuote_CreatesSpan`,
   `TestFetchStockQuote_SanitizesToken`,
   `TestDownloadTelegramPhoto_SanitizesToken`, `TestRateLimiter_Metric`.
9. Run `mise run test`, `mise run test-race`, `mise run lint`.

### Step 5: Gemini instrumentation + token metrics

1. Wrap the three `GenerateContent` call sites with manual `gemini.*` spans
   using `gen_ai.provider.name` / `gen_ai.operation.name` /
   `gen_ai.request.model`. Use **`gen_ai.operation.name="generate_content"`**
   for all three sites (review #5); distinguish them via the span name
   (`gemini.explain` / `gemini.classify` / `gemini.analyze`).
2. Extract `resp.UsageMetadata` (when present) and record
   `gen_ai.client.token.usage` (histogram) for input and output tokens with
   `gen_ai.token.type`.
3. Write `TestGeminiExplain_CreatesSpan` with a capturing mock generator that
   returns `UsageMetadata`.
4. Run `mise run test`, `mise run test-race`, `mise run lint`.

### Step 6: End-to-end verification against local collector

1. Set `OTEL_ENABLED=true` in `.env` (SOPS-encrypted).
2. Run `mise run run` and exercise `/lc`, `!s AAPL`, `!sa AAPL`, an `@bot`
   ask, and an x.com link.
3. Confirm in HyperDX/Clickstack:
   - Traces appear with handler span + child fetch/gemini spans.
   - **No `token=` or `bot<TOKEN>` value appears in any span OR log
     attribute** — verify this explicitly for both `url.full` and `http.url`
     on spans and `url`/`request.url` on logs (P0 acceptance criterion).
   - Metrics: `bot.commands.total`, `bot.command.duration`,
     `gen_ai.client.token.usage` appear.
   - Logs appear with correct severity and structured fields.
4. Run `mise run test`, `mise run test-race`, `mise run lint`.
5. Run `mise run test-integration` (once the task exists — see the note) and
   `mise test-integration 2>&1 | grep -w 'FAIL:'` per AGENTS.md before pushing.

## File Size Estimates

> **Non-normative.** Rough order-of-magnitude estimates for scoping only.

| File | Change | Est. lines |
|---|---|---|
| `internal/otel/providers.go` | `Providers`, `Exporters`, `NewProviders`, `Instruments` | ~130 |
| `internal/otel/setup.go` | env parsing, resource, global install, shutdown | ~130 |
| `internal/otel/traces.go` | tracer provider builder + OTLP/HTTP exporter | ~70 |
| `internal/otel/metrics.go` | meter provider builder + exporter + Instruments | ~110 |
| `internal/otel/logs.go` | logger provider builder + OTLP/HTTP exporter | ~70 |
| `internal/otel/sanitizer.go` | credential-stripping span exporter (redacts `url.full` + `http.url`) + shared `redactURL` helper | ~120 |
| `internal/otel/zerolog_bridge.go` | JSON → OTel log record writer + URL redaction for log attributes | ~140 |
| `internal/otel/http.go` | `NewHTTPTransport` + double-wrap guard | ~50 |
| `internal/otel/outcome.go` | outcome recorder helpers (mutex-guarded) | ~50 |
| `internal/otel/*_test.go` | unit tests with in-memory exporters + dual-key sanitizer tests + race test | ~560 |
| `internal/bot/bot.go` | middleware, transport rewire, registration helper | +100 |
| `internal/bot/stock.go` + `stock_fundamentals.go` + `leetcode.go` + `exa_search.go` + `parallel_search.go` + `ask.go` | manual spans at fetch sites + `recordOutcome` calls | +200 |
| `internal/bot/gemini_explainer.go` + `freshness_classifier.go` + `stock_analysis.go` | gemini spans + token histogram | +90 |
| `cmd/csy-helper-bot/main.go` | `Setup` call, `MultiWriter`, `defer shutdown()` | +20 |
| `go.mod` / `go.sum` | new deps | — |
| **Total** | | ~1,750 |

## Design Decisions Log

| # | Decision | Rationale |
|---|---|---|
| 1 | **Disabled by default (`OTEL_ENABLED=true` to opt in)** | CI and `mise run test` run without a collector. A disabled-by-default switch means zero config changes for existing pipelines and no exporter retries slowing tests. The user has a local HyperDX/Clickstack and sets the flag in `.env`. |
| 2 | **OTLP/HTTP, not gRPC** | Clickstack and HyperDX both ingest OTLP/HTTP on `:4318`. HTTP avoids the gRPC dependency, works through reverse proxies, and is simpler to configure behind Docker. gRPC remains available via standard `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` if a future collector needs it. |
| 3 | **New `internal/otel` package** | Telemetry is a single, independently-testable concern with no knowledge of bot logic. Matches the existing `internal/bot` layout and the "one concern per package" guideline. `internal/bot` imports `internal/otel`; the reverse is forbidden, preventing cycles. |
| 4 | **`otelhttp` transport + manual parent spans (two spans per HTTP call)** | `otelhttp` gives standard HTTP client semconv and metrics for free. A manual parent span (e.g. `finnhub.quote`) gives a domain-readable operation name in trace UIs. The parent/child pair is the recommended OTel pattern, not duplication. |
| 5 | **Manual spans for Gemini (not transport wrapping)** | The `google.golang.org/genai` SDK does not expose its HTTP transport, so we cannot wrap it. Wrapping the three `GenerateContent` call sites with GenAI semconv attributes is the cleanest boundary. |
| 6 | **Outcome recorder in context; default `bot.result=unknown` (P1)** | Handlers swallow errors and detect rate-limits/blocked/not-configured internally, so the outer middleware cannot infer the outcome. A context-carried `outcomeRecorder` lets handlers call `recordOutcome(ctx, "rate_limited")` at known decision points without changing signatures. The recorder is guarded by a `sync.Mutex` so a handler that calls `recordOutcome` from a goroutine does not race with the middleware's read (mandatory under `mise run test-race`, review #4). The default is `unknown`, **not** `success`, so dashboards never show a false green when the outcome was not positively confirmed. `result=error` is rarely set in v1 (handlers don't return errors); this is an honest three-valued metric (known-success / known-cause / unknown) until v4 adds handler error returns. Shipping a `result` dimension that is sometimes `unknown` is better than one that is silently, structurally wrong. |
| 7 | **zerolog bridge via `io.Writer`, not a `zerolog.Hook`** | zerolog hooks fire with an `*Event` whose fields are already serialized into an internal buffer that is not cleanly readable. A writer that parses the final JSON line has full access to every field and works regardless of the `ConsoleWriter` formatter. The contrib project does not ship a stable zerolog bridge as of writing. |
| 8 | **No trace ↔ log correlation in v1 (P2)** | The `io.Writer.Write` interface receives bytes, not `context.Context`, so the bridge **cannot** read the active span context — correlation cannot be implemented by the writer alone. Trace IDs must be injected into zerolog fields *before* serialization, via context-aware logging (`log.Ctx(ctx)`) or a zerolog hook that pulls the span context from a stored context. That is a refactor of every `log.*` call site and is out of scope for v1. See [Future Roadmap](#future-roadmap). |
| 9 | **Per-signal disable flags (`OTEL_TRACES_ENABLED` etc.)** | Lets operators keep cheap traces while disabling higher-volume logs/metrics, or vice versa. Each defaults to the master `OTEL_ENABLED` value. |
| 10 | **`stdout` exporter mode via `OTEL_EXPORTER=stdout`** | Local debugging without a collector. Uses the official `stdouttrace`/`stdoutmetric`/`stdoutlog` exporters. Distinct from `OTEL_ENABLED=false` (noop). |
| 11 | **`shutdown` guarded by `sync.Once` + 5s timeout** | `main` calls `shutdown()` on both the normal return path (`defer`) and the `log.Fatal` error path. `sync.Once` makes the double call safe; the timeout prevents a hung collector from blocking exit indefinitely. |
| 12 | **Test isolation: `NewProviders` + in-process `Setup` test (P1)** | `Setup` is the only function that mutates process-global providers. All other tests construct `*Providers` via `NewProviders` with in-memory exporters and assert on the struct — they never call `otel.Set*Provider`, so parallel tests cannot contaminate each other or retain instruments bound to a shutdown provider. The single global-install path is verified by one non-parallel in-process test that restores the prior providers in `t.Cleanup` (review #6 — subprocess is not needed since OTel's global setters are idempotent and the delegating global picks up the latest provider). |
| 13 | **Credential sanitizing span exporter + log bridge redaction (P0)** | `otelhttp` records the request URL as a span attribute, and 7 call sites (6 Finnhub + 1 Telegram) embed secrets in the URL. A sanitizing `SpanExporter` wrapper redacts `token`/`api_key`/`apikey`/`key` query values and `bot<TOKEN>` path segments before export, fail-closed on unparseable URLs. It redacts **both `url.full` and the legacy `http.url`** to stay robust to semconv version drift in the contrib (review #2). The zerolog bridge applies the same redaction to log records so a future `log.Error().Str("url", …)` cannot bypass the span sanitizer (review #3). This is defense-in-depth: it protects all spans/logs including future ones. The long-term fix (Finnhub header auth) is in the roadmap; the Telegram bot-token-in-path cannot be avoided, so the sanitizer is the permanent guard for that call. |
| 14 | **Safe span name `"<METHOD> <HOST>"`, never full URL** | The span name is the first thing visible in trace UIs. Using only method + host guarantees no credential leaks in the name regardless of the sanitizer. The URL attribute (sanitized) is in attributes, used for filtering — the sanitizer redacts both `url.full` and `http.url` so the contrib's semconv version does not matter. |
| 15 | **`gen_ai.client.token.usage` as a Histogram, matching semconv (P2)** | The OTel GenAI semconv defines this exact metric name as a histogram. Reusing a reserved semconv name with a different instrument type (counter) can break semconv-aware backends. A histogram still sums for "total spend per model" and gives per-request distributions. If a pure total is also wanted, add a clearly-custom `bot.gen_ai.tokens.total` counter — but the semconv name stays a histogram. |
| 16 | **Pin one semconv version; use `gen_ai.provider.name`** | The older `gen_ai.system` attribute is deprecated in newer semconv; `gen_ai.provider.name` is current. Pin `semconv/v1.30.0` (or latest stable at implementation time) across the project. The `otelhttp` contrib emits attributes per its own semconv version — verify compatibility at implementation time and align the project import if needed. If `gen_ai.*` constants are not generated in the pinned version, define string constants in `internal/otel` matching the spec. |
| 17 | **Handler span names are operation names; aliases collapse** | `/lc` and `!lc` both map to span `bot.lc` (the literal `/lc` vs `!lc` is in `bot.command.literal`). Same for `!s`/`!s ` → `bot.stock` and `!sa`/`!sa ` → `bot.stock_analysis`. ~9 fixed span names is low cardinality and far more readable than using the raw literal as the span name; the `bot.command` + `bot.command.literal` attributes enable exact filtering. |
| 18 | **Resource build info passed via `BuildInfo`, not imported from `main`** | `main` is package `main` and cannot be imported. `Setup` takes a small `BuildInfo{Commit, Date}` struct so `internal/otel` stays importable and testable. |
| 19 | **Telegram Bot API SDK calls not instrumented in v1** | `go-telegram/bot` owns its `http.Client` and does not expose it for transport wrapping. Handler spans already capture the user-facing latency including the Telegram round trips. Wrapping the SDK is a v2 enhancement. |
| 20 | **Transport rewire in explicit `init()` with double-wrap guard** | The package-level HTTP clients are rewired in `init()` wrapping their *original* transports. Double-wrapping (wrapping an already-otel-wrapped transport) would produce duplicate nested spans and double-counted metrics — prevented by wrapping the original transport only. Tests replace the whole `*http.Client` var, bypassing the wrapper; with a noop tracer (test default), nothing is emitted. |
| 21 | **Coverage floor is 40%, not 50%** | The CI workflow (`.github/workflows/ci.yml:143`) sets `THRESHOLD=40`. The plan targets ≥80% for `internal/otel` to stay comfortably above the floor. (AGENTS.md says 50%; that is a pre-existing doc inconsistency — the enforced CI value is 40%.) |
| 22 | **No new user-facing behavior** | Telemetry is observability-only. No new commands, no changed responses, no new error messages. Risk surface is limited to startup/shutdown and the noop fast path. |

## Future Roadmap

### v2 — Trace ↔ log correlation (corrected mechanism)

The v1 `io.Writer` bridge cannot read the active span context because
`Write([]byte) (int, error)` has no `context.Context` parameter. Correlation
requires injecting `trace_id`/`span_id` into the zerolog JSON *before*
serialization, then the writer reads those fields from the JSON. Two viable
approaches:

1. **Context-aware logging** — refactor `internal/bot` to use
   `log.Ctx(ctx).Info().…` with a zerolog logger that has a
   `zerolog.HookFunc` extracting the active span from the context and adding
   `trace_id`/`span_id` fields to every event. The writer then reads those
   fields and sets `Record.TraceID`/`Record.SpanID`. This is the cleanest path
   and requires touching every `log.*` call site (mechanical, large but
   low-risk).
2. **Span-context-in-context + hook** — pass the span context through the
   existing call chain via `context.Context` (already threaded through
   handlers/fetchers) and use a `zerolog.Hook` that reads it from a
   context-stored logger. Same writer-side behavior.

Either way, HyperDX/Clickstack then joins logs to traces automatically. This
is a focused refactor of the logging layer, not of telemetry.

### v3 — Telegram Bot API spans

Wrap `go-telegram/bot` calls by constructing the bot with a custom
`http.Client` whose transport is `otel.NewHTTPTransport`. Yields spans for
`SendMessage`, `EditMessageText`, `SendPhoto`, `GetFile`, `LeaveChat`,
`GetMe`, and the long-poll `GetUpdates` loop. The Telegram bot token appears
in the `GetUpdates` and `SendMessage` URL paths, so the sanitizer's
`bot<TOKEN>` redaction continues to guard these.

### v4 — Handler error returns + reliable `result=error`

Promote handlers to return `error` and have `tracingMiddleware` set span
status and `bot.commands.total{result=error}` from the return value. Removes
the `unknown`-default caveat from Decision #6 — `success` becomes the
positive default and `error` is reliably detected.

### v5 — Migrate Finnhub to header auth + exemplars

Move the Finnhub API key from the query string to a header
(`X-Finnhub-Token` or `Authorization: Bearer <key>`, if the API supports it),
removing the token from URLs entirely and reducing reliance on the sanitizer
for Finnhub. Enable histogram exemplars (OTel Go SDK supports exemplars) so
slow commands link directly to their trace in the metrics UI.

### v6 — OpenTelemetry Collector in Docker Compose + `test-integration` task

Ship a `docker-compose.otel.yml` with an OTel Collector (OTLP receiver →
HyperDX/Clickstack exporter) so contributors can `docker compose up` a full
local observability stack without installing anything else. Also resolve the
`test-integration` task inconsistency (add the task to `mise.toml` or update
AGENTS.md) noted in the [Test Plan](#verification-commands).
