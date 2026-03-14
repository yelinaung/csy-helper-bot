package bot

import (
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
