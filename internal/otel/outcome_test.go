package otel

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutcomeRecorder_DefaultUnknown(t *testing.T) {
	t.Parallel()

	ctx, recorder := withOutcomeRecorder(context.Background())
	require.NotNil(t, recorder)
	require.Equal(t, OutcomeUnknown, recorder.Result())
	// ctx carries the recorder.
	r, ok := ctx.Value(outcomeKey{}).(*outcomeRecorder)
	require.True(t, ok)
	require.Equal(t, recorder, r)
}

func TestOutcomeRecorder_SetResult(t *testing.T) {
	t.Parallel()

	ctx, recorder := withOutcomeRecorder(context.Background())
	require.Equal(t, OutcomeUnknown, recorder.Result())

	recordOutcome(ctx, "rate_limited")
	require.Equal(t, "rate_limited", recorder.Result())

	recordOutcome(ctx, "blocked")
	require.Equal(t, "blocked", recorder.Result())
}

func TestRecordOutcome_NoRecorderNoop(t *testing.T) {
	t.Parallel()

	// Calling RecordOutcome without a recorder must not panic.
	require.NotPanics(t, func() {
		recordOutcome(context.Background(), "success")
	})
}

func TestOutcomeRecorder_ConcurrentSafe(t *testing.T) {
	t.Parallel()

	ctx, recorder := withOutcomeRecorder(context.Background())

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				recordOutcome(ctx, "rate_limited")
			}
			_ = recorder.Result()
		}(i)
	}
	wg.Wait()

	// Final result is one of the recorded values — the key invariant is no
	// race, verified by mise run test-race.
	require.Contains(t, []string{"rate_limited"}, recorder.Result())
}

func TestOutcomeRecorder_NilResult(t *testing.T) {
	t.Parallel()

	var r *outcomeRecorder
	require.Equal(t, OutcomeUnknown, r.Result())
}

func TestExportedOutcomeAPI(t *testing.T) {
	t.Parallel()

	ctx, recorder := WithOutcomeRecorder(context.Background())
	RecordOutcome(ctx, "not_configured")
	require.Equal(t, "not_configured", recorder.Result())
}
