package otel

import (
	"context"
	"sync"
)

// OutcomeUnknown is the default result when a handler does not positively
// record an outcome. Dashboards never show a false green because the default
// is "unknown", not "success".
const OutcomeUnknown = "unknown"

type outcomeKey struct{}

// outcomeRecorder lets a handler communicate its outcome to the outer tracing
// middleware without changing its signature. The mutex guards result against
// handlers that spawn goroutines calling recordOutcome concurrently with the
// middleware's read.
type outcomeRecorder struct {
	mu     sync.Mutex
	result string
}

// withOutcomeRecorder returns a context carrying a fresh recorder (default
// result OutcomeUnknown) and the recorder itself for the middleware to read.
func withOutcomeRecorder(ctx context.Context) (context.Context, *outcomeRecorder) {
	r := &outcomeRecorder{result: OutcomeUnknown}
	return context.WithValue(ctx, outcomeKey{}, r), r
}

// WithOutcomeRecorder is the exported wrapper returning the context and the
// recorder's Result accessor. It is used by the tracing middleware in
// internal/bot.
func WithOutcomeRecorder(ctx context.Context) (context.Context, *outcomeRecorder) {
	return withOutcomeRecorder(ctx)
}

// recordOutcome sets the result on the recorder in the context, if present.
// It is a no-op when no recorder is in the context (e.g. called outside a
// handler) and safe for concurrent use.
func recordOutcome(ctx context.Context, result string) {
	r, ok := ctx.Value(outcomeKey{}).(*outcomeRecorder)
	if !ok || r == nil {
		return
	}
	r.mu.Lock()
	r.result = result
	r.mu.Unlock()
}

// RecordOutcome is the exported wrapper used by handlers to communicate their
// outcome (e.g. "rate_limited", "not_configured", "blocked") to the outer
// tracing middleware.
func RecordOutcome(ctx context.Context, result string) {
	recordOutcome(ctx, result)
}

// Result returns the current outcome under the mutex.
func (r *outcomeRecorder) Result() string {
	if r == nil {
		return OutcomeUnknown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}
