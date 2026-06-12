package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withParallelTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	original := parallelSearchBaseURL
	parallelSearchBaseURL = server.URL
	t.Cleanup(func() { parallelSearchBaseURL = original })
}

func TestSearchParallel_Success(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "test-key")

	var gotRequest parallelSearchRequest
	withParallelTestServer(t, func(w http.ResponseWriter, r *http.Request) {
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

	results, err := searchParallel(context.Background(), "find latest Go release", []string{"go latest release version"})
	if err != nil {
		t.Fatalf("searchParallel() error = %v", err)
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

func TestSearchParallel_MissingAPIKey(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "  ")

	_, err := searchParallel(context.Background(), "anything", nil)
	if err == nil || !strings.Contains(err.Error(), "PARALLEL_API_KEY") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func TestSearchParallel_EmptyObjective(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "test-key")

	_, err := searchParallel(context.Background(), "  ", nil)
	if err == nil || !strings.Contains(err.Error(), "objective") {
		t.Fatalf("expected objective error, got %v", err)
	}
}

func TestSearchParallel_Non200(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "test-key")

	withParallelTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := searchParallel(context.Background(), "anything", []string{"query"})
	if err == nil || !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected status error, got %v", err)
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

func TestParallelSearchEnabled(t *testing.T) {
	t.Setenv("PARALLEL_API_KEY", "")
	if parallelSearchEnabled() {
		t.Error("expected disabled with empty key")
	}

	t.Setenv("PARALLEL_API_KEY", "key")
	if !parallelSearchEnabled() {
		t.Error("expected enabled with key set")
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
