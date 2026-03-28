package bot

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultExplainRateLimitCount  = 5
	defaultExplainRateLimitWindow = time.Minute
)

type rateEntry struct {
	windowStart time.Time
	count       int
}

type memoryRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	data   map[string]rateEntry
}

func newMemoryRateLimiter(limit int, window time.Duration) *memoryRateLimiter {
	if limit <= 0 {
		limit = defaultExplainRateLimitCount
	}
	if window <= 0 {
		window = defaultExplainRateLimitWindow
	}
	return &memoryRateLimiter{
		limit:  limit,
		window: window,
		data:   make(map[string]rateEntry),
	}
}

func (r *memoryRateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	if r == nil {
		return true, 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.data[key]
	if !ok || now.Sub(entry.windowStart) >= r.window {
		r.data[key] = rateEntry{windowStart: now, count: 1}
		return true, 0
	}

	if entry.count < r.limit {
		entry.count++
		r.data[key] = entry
		return true, 0
	}

	retryAfter := r.window - now.Sub(entry.windowStart)
	retryAfter = max(retryAfter, 0)
	return false, retryAfter
}

func loadExplainRateLimiter() *memoryRateLimiter {
	limit := defaultExplainRateLimitCount
	window := defaultExplainRateLimitWindow

	if raw := getenvTrim("EXPLAIN_RATE_LIMIT_COUNT"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	if raw := getenvTrim("EXPLAIN_RATE_LIMIT_WINDOW_SECONDS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			window = time.Duration(n) * time.Second
		}
	}

	return newMemoryRateLimiter(limit, window)
}

func buildExplainRateKey(chatID, userID int64) string {
	if userID != 0 {
		return fmt.Sprintf("chat:%d:user:%d", chatID, userID)
	}
	return fmt.Sprintf("chat:%d", chatID)
}

func getenvTrim(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}
