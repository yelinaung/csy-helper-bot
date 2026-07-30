package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestParallelExtractor starts a local server and returns an extractor
// pointed at it, avoiding any shared package-level state.
func newTestParallelExtractor(t *testing.T, handler http.HandlerFunc) *parallelExtractor {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &parallelExtractor{
		baseURL: server.URL,
		apiKey:  "test-key",
		timeout: 5 * time.Second,
		maxURLs: defaultExtractMaxURLs,
	}
}

func TestParallelExtractor_Success(t *testing.T) {
	t.Parallel()

	var gotRequest parallelExtractRequest
	extractor := newTestParallelExtractor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("expected x-api-key header, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}

		resp := parallelExtractResponse{
			ExtractID: "extract-123",
			Results: []parallelExtractResult{
				{
					URL:         "https://example.com/plans",
					Title:       "Pricing",
					PublishDate: "2026-01-01",
					Excerpts:    []string{"The Pro plan costs $10/month."},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	results, err := extractor.extract(context.Background(), []string{"https://example.com/plans"}, "what are the plan prices?")
	if err != nil {
		t.Fatalf("extract() error = %v", err)
	}

	if len(gotRequest.URLs) != 1 || gotRequest.URLs[0] != "https://example.com/plans" {
		t.Errorf("urls = %v", gotRequest.URLs)
	}
	if gotRequest.Objective != "what are the plan prices?" {
		t.Errorf("objective = %q", gotRequest.Objective)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].URL != "https://example.com/plans" || results[0].Title != "Pricing" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestParallelExtractor_NilExtractor(t *testing.T) {
	t.Parallel()

	var extractor *parallelExtractor

	_, err := extractor.extract(context.Background(), []string{"https://example.org"}, "anything")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected not configured error, got %v", err)
	}
}

func TestParallelExtractor_EmptyObjective(t *testing.T) {
	t.Parallel()

	extractor := newTestParallelExtractor(t, func(http.ResponseWriter, *http.Request) {})

	_, err := extractor.extract(context.Background(), []string{"https://example.org"}, "  ")
	if err == nil || !strings.Contains(err.Error(), "objective") {
		t.Fatalf("expected objective error, got %v", err)
	}
}

func TestParallelExtractor_EmptyURLs(t *testing.T) {
	t.Parallel()

	extractor := newTestParallelExtractor(t, func(http.ResponseWriter, *http.Request) {})

	_, err := extractor.extract(context.Background(), nil, "anything")
	if err == nil || !strings.Contains(err.Error(), "URL is required") {
		t.Fatalf("expected URL required error, got %v", err)
	}
}

func TestParallelExtractor_Non200(t *testing.T) {
	t.Parallel()

	extractor := newTestParallelExtractor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": "quota exceeded"}`))
	})

	_, err := extractor.extract(context.Background(), []string{"https://example.org"}, "anything")
	if err == nil || !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected status error, got %v", err)
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("expected error to include response body, got %v", err)
	}
}

func TestParallelExtractor_HonorsConfiguredTimeout(t *testing.T) {
	t.Parallel()

	// Drain the body first: the server only watches for client disconnect
	// (which cancels the request context) once the body is consumed. Then
	// hold the request open until the client times out, so the test server
	// shuts down cleanly afterwards.
	extractor := newTestParallelExtractor(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	extractor.timeout = 50 * time.Millisecond

	start := time.Now()
	_, err := extractor.extract(context.Background(), []string{"https://example.org"}, "anything")
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("request took %v, configured timeout not honored", elapsed)
	}
}

func TestParallelExtractor_InvalidJSON(t *testing.T) {
	t.Parallel()

	extractor := newTestParallelExtractor(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})

	_, err := extractor.extract(context.Background(), []string{"https://example.org"}, "anything")
	if err == nil || !strings.Contains(err.Error(), "decode parallel extract response") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

// TestParallelExtractor_PerURLErrorsSkippedNotFatal asserts that per-URL
// entries in the documented Errors field (error_type/http_status_code/content,
// not a made-up "message" field) are logged and skipped, while the request
// as a whole still succeeds with whatever results came back.
func TestParallelExtractor_PerURLErrorsSkippedNotFatal(t *testing.T) {
	t.Parallel()

	status := 404
	extractor := newTestParallelExtractor(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := parallelExtractResponse{
			ExtractID: "extract-mixed",
			Results: []parallelExtractResult{
				{URL: "https://example.com/ok", Title: "OK", Excerpts: []string{"Some content."}},
			},
			Errors: []parallelExtractError{
				{URL: "https://example.com/dead", ErrorType: "fetch_error", HTTPStatusCode: &status, Content: "not found"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	results, err := extractor.extract(context.Background(), []string{"https://example.com/ok", "https://example.com/dead"}, "anything")
	if err != nil {
		t.Fatalf("extract() error = %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://example.com/ok" {
		t.Fatalf("expected only the successful result, got %+v", results)
	}
}

func TestNewParallelExtractor(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "  ")
	t.Setenv("EXTRACT_ENABLED", "")
	if newParallelExtractor() != nil {
		t.Error("expected nil extractor with blank key")
	}

	t.Setenv("PARALLEL_API_KEY", "key")
	t.Setenv("EXTRACT_TIMEOUT_SECONDS", "45")
	t.Setenv("EXTRACT_MAX_URLS", "5")
	extractor := newParallelExtractor()
	if extractor == nil {
		t.Fatal("expected extractor with key set")
	}
	if extractor.baseURL != defaultParallelExtractBaseURL {
		t.Errorf("baseURL = %q", extractor.baseURL)
	}
	if extractor.apiKey != "key" {
		t.Errorf("apiKey = %q", extractor.apiKey)
	}
	if extractor.timeout != 45*time.Second {
		t.Errorf("timeout = %v", extractor.timeout)
	}
	if extractor.maxURLs != 5 {
		t.Errorf("maxURLs = %d", extractor.maxURLs)
	}
}

func TestNewParallelExtractor_KillSwitchDisabled(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "key")
	t.Setenv("EXTRACT_ENABLED", "false")

	if newParallelExtractor() != nil {
		t.Error("expected nil extractor when EXTRACT_ENABLED=false, even with a key configured")
	}
}

func TestNewParallelExtractor_KillSwitchInvalidValueDefaultsEnabled(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "key")
	t.Setenv("EXTRACT_ENABLED", "banana")

	if newParallelExtractor() == nil {
		t.Error("expected extractor to remain enabled on an unparsable EXTRACT_ENABLED value")
	}
}

func TestSanitizeParallelExtractResults(t *testing.T) {
	t.Parallel()

	longExcerpt := strings.Repeat("က", maxParallelExtractExcerptRuneLen+50)

	results := sanitizeParallelExtractResults([]parallelExtractResult{
		{
			URL:      "https://example.com/first",
			Title:    "Kept",
			Excerpts: []string{"one", "two", "three", "four", "five"},
		},
		{
			URL:      "https://example.com/second",
			Excerpts: []string{longExcerpt},
		},
		{
			URL:   "https://example.com/title-only",
			Title: "Has a title but no excerpts",
		},
		{
			URL: "https://example.com/empty",
		},
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 results (title-only and fully-empty dropped), got %d: %+v", len(results), results)
	}
	if len(results[0].Excerpts) != maxParallelExtractExcerptsPerItem {
		t.Errorf("expected excerpts capped at %d, got %d", maxParallelExtractExcerptsPerItem, len(results[0].Excerpts))
	}
	if got := runeLen(results[1].Excerpts[0]); got != maxParallelExtractExcerptRuneLen {
		t.Errorf("expected excerpt truncated to %d runes, got %d", maxParallelExtractExcerptRuneLen, got)
	}
}

func TestLoadExtractTimeout(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"empty defaults", "", defaultParallelExtractTimeout},
		{"invalid defaults", "not-a-number", defaultParallelExtractTimeout},
		{"zero defaults", "0", defaultParallelExtractTimeout},
		{"valid", "45", 45 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EXTRACT_TIMEOUT_SECONDS", tt.value)
			if got := loadExtractTimeout(); got != tt.want {
				t.Errorf("loadExtractTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadExtractMaxURLs(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{"empty defaults", "", defaultExtractMaxURLs},
		{"invalid defaults", "not-a-number", defaultExtractMaxURLs},
		{"negative defaults", "-1", defaultExtractMaxURLs},
		{"valid", "5", 5},
		{"capped", "50", extractMaxURLsCap},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EXTRACT_MAX_URLS", tt.value)
			if got := loadExtractMaxURLs(); got != tt.want {
				t.Errorf("loadExtractMaxURLs() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLoadExtractEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty defaults to enabled", "", true},
		{"true", "true", true},
		{"false", "false", false},
		{"invalid defaults to enabled", "banana", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EXTRACT_ENABLED", tt.value)
			if got := loadExtractEnabled(); got != tt.want {
				t.Errorf("loadExtractEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
