package bot

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

func TestMemoryRateLimiterAllow(t *testing.T) {
	rl := newMemoryRateLimiter(2, time.Minute)
	now := time.Now()

	ok, _ := rl.allow("k", now)
	if !ok {
		t.Fatal("first request should pass")
	}
	ok, _ = rl.allow("k", now.Add(10*time.Second))
	if !ok {
		t.Fatal("second request should pass")
	}
	ok, retry := rl.allow("k", now.Add(20*time.Second))
	if ok {
		t.Fatal("third request should be rate limited")
	}
	if retry <= 0 {
		t.Fatalf("expected positive retry duration, got %v", retry)
	}
}

func TestMemoryRateLimiterWindowReset(t *testing.T) {
	rl := newMemoryRateLimiter(1, 10*time.Second)
	now := time.Now()

	ok, _ := rl.allow("k", now)
	if !ok {
		t.Fatal("first request should pass")
	}
	ok, _ = rl.allow("k", now.Add(11*time.Second))
	if !ok {
		t.Fatal("request after window should pass")
	}
}

func TestAllowExplainRequest(t *testing.T) {
	prev := explainLimiter
	explainLimiter = newMemoryRateLimiter(1, time.Minute)
	defer func() { explainLimiter = prev }()

	msg := &models.Message{
		Chat: models.Chat{ID: -1001},
		From: &models.User{ID: 77},
	}

	allowed, _ := allowExplainRequest(msg)
	if !allowed {
		t.Fatal("first request should pass")
	}
	allowed, _ = allowExplainRequest(msg)
	if allowed {
		t.Fatal("second request should be limited")
	}
}

func TestLoadExplainRateLimiter(t *testing.T) {
	t.Run("defaults when no env vars", func(t *testing.T) {
		t.Setenv("EXPLAIN_RATE_LIMIT_COUNT", "")
		t.Setenv("EXPLAIN_RATE_LIMIT_WINDOW_SECONDS", "")

		rl := loadExplainRateLimiter()
		if rl.limit != defaultExplainRateLimitCount {
			t.Fatalf("expected limit %d, got %d", defaultExplainRateLimitCount, rl.limit)
		}
		if rl.window != defaultExplainRateLimitWindow {
			t.Fatalf("expected window %v, got %v", defaultExplainRateLimitWindow, rl.window)
		}
	})

	t.Run("custom values", func(t *testing.T) {
		t.Setenv("EXPLAIN_RATE_LIMIT_COUNT", "10")
		t.Setenv("EXPLAIN_RATE_LIMIT_WINDOW_SECONDS", "120")

		rl := loadExplainRateLimiter()
		if rl.limit != 10 {
			t.Fatalf("expected limit 10, got %d", rl.limit)
		}
		if rl.window != 120*time.Second {
			t.Fatalf("expected window 120s, got %v", rl.window)
		}
	})

	t.Run("invalid values fall back to defaults", func(t *testing.T) {
		t.Setenv("EXPLAIN_RATE_LIMIT_COUNT", "notanumber")
		t.Setenv("EXPLAIN_RATE_LIMIT_WINDOW_SECONDS", "-5")

		rl := loadExplainRateLimiter()
		if rl.limit != defaultExplainRateLimitCount {
			t.Fatalf("expected default limit %d, got %d", defaultExplainRateLimitCount, rl.limit)
		}
		if rl.window != defaultExplainRateLimitWindow {
			t.Fatalf("expected default window %v, got %v", defaultExplainRateLimitWindow, rl.window)
		}
	})
}

func TestGetenvTrim(t *testing.T) {
	t.Run("not set", func(t *testing.T) {
		got := getenvTrim("TEST_GETENV_TRIM_UNSET_VAR_XYZ")
		if got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})

	t.Run("set with whitespace", func(t *testing.T) {
		t.Setenv("TEST_GETENV_TRIM_WS", "  hello  ")
		got := getenvTrim("TEST_GETENV_TRIM_WS")
		if got != "hello" {
			t.Fatalf("expected %q, got %q", "hello", got)
		}
	})

	t.Run("normal value", func(t *testing.T) {
		t.Setenv("TEST_GETENV_TRIM_NORMAL", "world")
		got := getenvTrim("TEST_GETENV_TRIM_NORMAL")
		if got != "world" {
			t.Fatalf("expected %q, got %q", "world", got)
		}
	})
}

func TestMemoryRateLimiter_Sweep(t *testing.T) {
	rl := newMemoryRateLimiter(1, 10*time.Second)
	now := time.Now()

	// Fill the map to capacity through allow().
	for i := range rateLimitMaxMapSize {
		key := fmt.Sprintf("user:%d", i)
		ok, _ := rl.allow(key, now)
		if !ok {
			t.Fatalf("first request for key %d should pass", i)
		}
	}

	// All entries are at capacity — new keys are rejected.
	ok, _ := rl.allow("overflow:1", now)
	if ok {
		t.Fatal("expected overflow key to be rejected at capacity")
	}

	// Move time forward past the window so all entries are expired.
	future := now.Add(20 * time.Second)

	// One more request should trigger the sweep, freeing space.
	ok, _ = rl.allow("newuser:1", future)
	if !ok {
		t.Fatal("request with a fresh key should pass after sweep")
	}

	// After sweep, the map should be much smaller.
	rl.mu.Lock()
	mapLen := len(rl.data)
	rl.mu.Unlock()

	if mapLen >= rateLimitMaxMapSize {
		t.Fatalf("expected sweep to reduce map size below %d, got %d", rateLimitMaxMapSize, mapLen)
	}
}

func TestMemoryRateLimiter_Sweep_Partial(t *testing.T) {
	rl := newMemoryRateLimiter(1, 10*time.Second)
	now := time.Now()

	expiredKey := "user:expired"
	activeKey := "user:active"

	expiredStart := now.Add(-2 * rl.window)
	activeStart := now

	// Seed one expired and one active entry.
	ok, _ := rl.allow(expiredKey, expiredStart)
	if !ok {
		t.Fatal("first request for expiredKey should pass")
	}
	ok, _ = rl.allow(activeKey, activeStart)
	if !ok {
		t.Fatal("first request for activeKey should pass")
	}

	// Sweep at a point where expiredKey is expired but activeKey still
	// within its window.
	sweepTime := activeStart.Add(rl.window / 2)
	rl.mu.Lock()
	rl.sweepLocked(sweepTime)
	rl.mu.Unlock()

	// Expired entry should be gone — a fresh request for it passes again.
	ok, _ = rl.allow(expiredKey, sweepTime)
	if !ok {
		t.Fatal("expiredKey should have been removed and allowed again after sweep")
	}

	// Active entry (limit=1) should still block a second request.
	ok, _ = rl.allow(activeKey, sweepTime)
	if ok {
		t.Fatal("activeKey should still be rate-limited after sweep")
	}
}

// TestMemoryRateLimiter_ClockSkewClampsRetryAfter verifies that a
// backwards clock movement does not produce a retryAfter longer than the
// window. Before the fix, max(retryAfter, 0) only floored negative values
// at zero; when now moved backwards past windowStart, now.Sub(windowStart)
// was negative and retryAfter = window - (-skew) = window + skew, which
// exceeded the window and misled the user.
func TestMemoryRateLimiter_ClockSkewClampsRetryAfter(t *testing.T) {
	rl := newMemoryRateLimiter(1, 10*time.Second)
	t0 := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	// Fill the limit=1 bucket at t0.
	ok, _ := rl.allow("k", t0)
	if !ok {
		t.Fatal("first request should pass")
	}
	// Second request at t0 is rejected with retryAfter = window.
	ok, retry := rl.allow("k", t0)
	if ok {
		t.Fatal("second request at limit should be rejected")
	}
	if retry != 10*time.Second {
		t.Fatalf("retry at t0 = %v, want 10s", retry)
	}

	// Move the clock backwards by 5 seconds. The naive computation would
	// produce retryAfter = 10s - (-5s) = 15s, which exceeds the window.
	ok, retry = rl.allow("k", t0.Add(-5*time.Second))
	if ok {
		t.Fatal("request with backwards clock should still be rejected")
	}
	if retry > rl.window {
		t.Fatalf("retryAfter %v exceeds window %v on backwards clock", retry, rl.window)
	}
	if retry < 0 {
		t.Fatalf("retryAfter %v is negative", retry)
	}
}
