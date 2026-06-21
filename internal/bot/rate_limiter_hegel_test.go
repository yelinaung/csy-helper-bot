package bot

import (
	"fmt"
	"testing"
	"time"

	"hegel.dev/go/hegel"
)

// rateLimiterMachine is a Hegel stateful-test machine that mirrors
// memoryRateLimiter.allow and sweepLocked against a reference map. Each
// test case starts with a fresh subject and model; rules draw a key and a
// clock offset, apply the operation to both, and assert they agree.
//
// The clock offset is drawn from [-3600, 3600] seconds relative to a fixed
// base time so the clock moves both forward and backward — exercising the
// retryAfter clamp that Bug 3 fixed.
type rateLimiterMachine struct {
	subject  *memoryRateLimiter
	model    map[string]rateEntry
	baseTime time.Time
}

// modelAllow mirrors memoryRateLimiter.allow without the mutex or the
// capacity-sweep side effect. It returns the same (ok, retryAfter) the
// subject should return for the same inputs, so the rule can compare.
func (m *rateLimiterMachine) modelAllow(key string, now time.Time) (bool, time.Duration) {
	entry, ok := m.model[key]
	if !ok || now.Sub(entry.windowStart) >= m.subject.window {
		// The subject's capacity cap and sweep only fire for new keys
		// when the map is at rateLimitMaxMapSize. Tests use small key
		// alphabets so this path is unreachable in the model; if it
		// ever fires, the subject may reject where the model accepts,
		// and the rule will surface that as a disagreement worth
		// investigating rather than silently masking.
		m.model[key] = rateEntry{windowStart: now, count: 1}
		return true, 0
	}
	if entry.count < m.subject.limit {
		entry.count++
		m.model[key] = entry
		return true, 0
	}
	retryAfter := m.subject.window - now.Sub(entry.windowStart)
	retryAfter = min(max(retryAfter, 0), m.subject.window)
	return false, retryAfter
}

// modelSweep mirrors sweepLocked: delete entries whose window has expired
// relative to now.
func (m *rateLimiterMachine) modelSweep(now time.Time) {
	for key, entry := range m.model {
		if now.Sub(entry.windowStart) >= m.subject.window {
			delete(m.model, key)
		}
	}
}

// RuleAllow draws a key and a clock offset, calls allow on both the
// subject and the model, and asserts they agree on (ok, retryAfter).
func (m *rateLimiterMachine) RuleAllow(tc hegel.TestCase) {
	key := hegel.Draw(tc, hegel.SampledFrom([]string{"a", "b", "c", "d", "e"}))
	offsetSec := hegel.Draw(tc, hegel.Integers(-3600, 3600))
	now := m.baseTime.Add(time.Duration(offsetSec) * time.Second)

	subjectOK, subjectRetry := m.subject.allow(key, now)
	modelOK, modelRetry := m.modelAllow(key, now)

	if subjectOK != modelOK {
		panicf("allow(%q, %v): subject ok=%v, model ok=%v", key, now, subjectOK, modelOK)
	}
	if subjectOK {
		// retryAfter is 0 when allowed by both.
		if subjectRetry != 0 || modelRetry != 0 {
			panicf("allow(%q, %v): allowed but retryAfter subject=%v model=%v",
				key, now, subjectRetry, modelRetry)
		}
	} else {
		// Invariant: 0 <= retryAfter <= window (Bug 3).
		if subjectRetry < 0 || subjectRetry > m.subject.window {
			panicf("allow(%q, %v): retryAfter %v outside [0, %v]",
				key, now, subjectRetry, m.subject.window)
		}
		if subjectRetry != modelRetry {
			panicf("allow(%q, %v): retryAfter subject=%v model=%v",
				key, now, subjectRetry, modelRetry)
		}
	}
}

// RuleSweep draws a clock offset, calls sweepLocked on the subject and
// prunes the model, then asserts the two agree on which keys remain.
func (m *rateLimiterMachine) RuleSweep(tc hegel.TestCase) {
	offsetSec := hegel.Draw(tc, hegel.Integers(-3600, 3600))
	now := m.baseTime.Add(time.Duration(offsetSec) * time.Second)

	m.subject.mu.Lock()
	m.subject.sweepLocked(now)
	m.subject.mu.Unlock()

	m.modelSweep(now)

	// Compare key sets. A disagreement means sweepLocked deleted a key
	// the model kept (or vice versa), which is a real bug.
	if len(m.subject.data) != len(m.model) {
		panicf("sweep(%v): subject has %d keys, model has %d",
			now, len(m.subject.data), len(m.model))
	}
	for key, subjectEntry := range m.subject.data {
		modelEntry, ok := m.model[key]
		if !ok {
			panicf("sweep(%v): subject has key %q, model does not", now, key)
		}
		if subjectEntry != modelEntry {
			panicf("sweep(%v): key %q subject=%+v model=%+v",
				now, key, subjectEntry, modelEntry)
		}
	}
}

// InvariantMapSize asserts the subject never exceeds the capacity cap.
func (m *rateLimiterMachine) InvariantMapSize(_ hegel.TestCase) {
	if len(m.subject.data) > rateLimitMaxMapSize {
		panicf("subject map size %d exceeds cap %d", len(m.subject.data), rateLimitMaxMapSize)
	}
}

// InvariantModelAgrees asserts every subject entry matches the model
// after every rule. This catches stale-state bugs where an update modifies
// one index but not another.
func (m *rateLimiterMachine) InvariantModelAgrees(_ hegel.TestCase) {
	if len(m.subject.data) != len(m.model) {
		panicf("subject has %d keys, model has %d", len(m.subject.data), len(m.model))
	}
	for key, subjectEntry := range m.subject.data {
		modelEntry, ok := m.model[key]
		if !ok {
			panicf("subject has key %q, model does not", key)
		}
		if subjectEntry != modelEntry {
			panicf("key %q subject=%+v model=%+v", key, subjectEntry, modelEntry)
		}
	}
}

func panicf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}

// TestRateLimiterStateful runs the model-based property test against
// memoryRateLimiter. The machine exercises allow and sweep with a clock
// that moves both forward and backward, catching the retryAfter > window
// bug (Bug 3) and any stale-state disagreement between subject and model.
func TestRateLimiterStateful(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		const limit = 3
		window := 10 * time.Second
		m := &rateLimiterMachine{
			subject:  newMemoryRateLimiter(limit, window),
			model:    make(map[string]rateEntry),
			baseTime: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
		}
		hegel.RunStateful(ht, m)
	}, hegel.WithTestCases(200))
}
