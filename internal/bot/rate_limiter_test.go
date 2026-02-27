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

	if !allowExplainRequest(msg) {
		t.Fatal("first request should pass")
	}
	if allowExplainRequest(msg) {
		t.Fatal("second request should be limited")
	}
}
