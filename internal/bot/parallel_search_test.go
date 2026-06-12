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

// newTestParallelSearcher starts a local server and returns a searcher
// pointed at it, avoiding any shared package-level state.
func newTestParallelSearcher(t *testing.T, handler http.HandlerFunc) *parallelSearcher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &parallelSearcher{
		baseURL:    server.URL,
		apiKey:     "test-key",
		timeout:    5 * time.Second,
		maxResults: defaultParallelMaxResults,
	}
}

func TestParallelSearcher_Success(t *testing.T) {
	var gotRequest parallelSearchRequest
	searcher := newTestParallelSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("expected x-api-key header, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}

		resp := parallelSearchResponse{
			SearchID: "search-123",
			Results: []parallelSearchResult{
				{
					URL:         "https://example.com/news",
					Title:       "Go 1.27 released",
					PublishDate: "2026-06-01",
					Excerpts:    []string{"Go 1.27 ships with faster GC."},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	results, err := searcher.search(context.Background(), "find latest Go release", []string{"go latest release version"})
	if err != nil {
		t.Fatalf("search() error = %v", err)
	}

	if gotRequest.Objective != "find latest Go release" {
		t.Errorf("objective = %q", gotRequest.Objective)
	}
	if len(gotRequest.SearchQueries) != 1 || gotRequest.SearchQueries[0] != "go latest release version" {
		t.Errorf("search_queries = %v", gotRequest.SearchQueries)
	}
	if gotRequest.AdvancedSettings == nil || gotRequest.AdvancedSettings.MaxResults != defaultParallelMaxResults {
		t.Errorf("advanced_settings = %+v, want max_results %d", gotRequest.AdvancedSettings, defaultParallelMaxResults)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].URL != "https://example.com/news" || results[0].Title != "Go 1.27 released" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestParallelSearcher_NilSearcher(t *testing.T) {
	var searcher *parallelSearcher

	_, err := searcher.search(context.Background(), "anything", nil)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected not configured error, got %v", err)
	}
}

func TestParallelSearcher_EmptyObjective(t *testing.T) {
	searcher := newTestParallelSearcher(t, func(http.ResponseWriter, *http.Request) {})

	_, err := searcher.search(context.Background(), "  ", nil)
	if err == nil || !strings.Contains(err.Error(), "objective") {
		t.Fatalf("expected objective error, got %v", err)
	}
}

func TestParallelSearcher_Non200(t *testing.T) {
	searcher := newTestParallelSearcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": "quota exceeded"}`))
	})

	_, err := searcher.search(context.Background(), "anything", []string{"query"})
	if err == nil || !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected status error, got %v", err)
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("expected error to include response body, got %v", err)
	}
}

func TestParallelSearcher_HonorsConfiguredTimeout(t *testing.T) {
	// Drain the body first: the server only watches for client disconnect
	// (which cancels the request context) once the body is consumed. Then
	// hold the request open until the client times out, so the test server
	// shuts down cleanly afterwards.
	searcher := newTestParallelSearcher(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	searcher.timeout = 50 * time.Millisecond

	start := time.Now()
	_, err := searcher.search(context.Background(), "anything", []string{"query"})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline error, got %v", err)
	}
	// The searcher's own timeout must govern the request; the shared
	// 10s httpClient would not have fired yet.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("request took %v, configured timeout not honored", elapsed)
	}
}

func TestParallelSearcher_InvalidJSON(t *testing.T) {
	searcher := newTestParallelSearcher(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})

	_, err := searcher.search(context.Background(), "anything", []string{"query"})
	if err == nil || !strings.Contains(err.Error(), "decode parallel search response") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestNewParallelSearcher(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "  ")
	if searcher := newParallelSearcher(); searcher != nil {
		t.Error("expected nil searcher with blank key")
	}

	t.Setenv("PARALLEL_API_KEY", "key")
	t.Setenv("PARALLEL_TIMEOUT_SECONDS", "30")
	t.Setenv("PARALLEL_MAX_RESULTS", "8")
	searcher := newParallelSearcher()
	if searcher == nil {
		t.Fatal("expected searcher with key set")
	}
	if searcher.baseURL != defaultParallelSearchBaseURL {
		t.Errorf("baseURL = %q", searcher.baseURL)
	}
	if searcher.apiKey != "key" {
		t.Errorf("apiKey = %q", searcher.apiKey)
	}
	if searcher.timeout != 30*time.Second {
		t.Errorf("timeout = %v", searcher.timeout)
	}
	if searcher.maxResults != 8 {
		t.Errorf("maxResults = %d", searcher.maxResults)
	}
}

func TestSanitizeParallelResults(t *testing.T) {
	longExcerpt := strings.Repeat("က", maxParallelExcerptRuneLen+50)

	results := sanitizeParallelResults([]parallelSearchResult{
		{
			URL:      "https://example.com/a",
			Title:    "Kept",
			Excerpts: []string{"one", "two", "three", "four"},
		},
		{
			URL:      "https://example.com/b",
			Excerpts: []string{longExcerpt},
		},
		{
			URL: "https://example.com/dropped",
		},
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if len(results[0].Excerpts) != maxParallelExcerptsPerItem {
		t.Errorf("expected excerpts capped at %d, got %d", maxParallelExcerptsPerItem, len(results[0].Excerpts))
	}
	if got := runeLen(results[1].Excerpts[0]); got != maxParallelExcerptRuneLen {
		t.Errorf("expected excerpt truncated to %d runes, got %d", maxParallelExcerptRuneLen, got)
	}
}

func TestLoadParallelTimeout(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"empty defaults", "", defaultParallelTimeout},
		{"invalid defaults", "abc", defaultParallelTimeout},
		{"zero defaults", "0", defaultParallelTimeout},
		{"valid", "30", 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PARALLEL_TIMEOUT_SECONDS", tt.value)
			if got := loadParallelTimeout(); got != tt.want {
				t.Errorf("loadParallelTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadParallelMaxResults(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{"empty defaults", "", defaultParallelMaxResults},
		{"invalid defaults", "abc", defaultParallelMaxResults},
		{"negative defaults", "-1", defaultParallelMaxResults},
		{"valid", "8", 8},
		{"capped", "50", parallelMaxResultsCap},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PARALLEL_MAX_RESULTS", tt.value)
			if got := loadParallelMaxResults(); got != tt.want {
				t.Errorf("loadParallelMaxResults() = %d, want %d", got, tt.want)
			}
		})
	}
}
