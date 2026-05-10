package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func resetExaCacheForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		exaCacheMu.Lock()
		exaCache = map[string]cachedExaResults{}
		exaCacheMu.Unlock()
	})
	exaCacheMu.Lock()
	exaCache = map[string]cachedExaResults{}
	exaCacheMu.Unlock()
}

func TestSearchStockNews_Success(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")
	t.Setenv("EXA_NUM_RESULTS", "3")
	resetExaCacheForTest(t)

	mockResp := exaSearchResponse{
		RequestID: "req-123",
		Results: []exaSearchResult{
			{
				Title:         "Apple Reports Record Q2 Revenue",
				URL:           "https://example.com/apple-q2",
				PublishedDate: "2026-05-01",
				Author:        "Jane Doe",
				Highlights:    []string{"Apple reported record Q2 revenue of $95 billion.", "iPhone sales exceeded expectations."},
			},
		},
	}
	mockResp.CostDollars.Total = 0.005

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key=test-key, got %s", r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResp)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	results, err := searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "Apple Reports Record Q2 Revenue" {
		t.Errorf("expected title 'Apple Reports Record Q2 Revenue', got %q", results[0].Title)
	}
	if results[0].URL != "https://example.com/apple-q2" {
		t.Errorf("expected URL preserved, got %q", results[0].URL)
	}
	if requestCount != 1 {
		t.Errorf("expected 1 HTTP request, got %d", requestCount)
	}
}

func TestSearchStockNews_EmptyResults(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")
	resetExaCacheForTest(t)

	mockResp := exaSearchResponse{
		RequestID: "req-empty",
		Results:   []exaSearchResult{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResp)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	results, err := searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchStockNews_ServerError(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")
	resetExaCacheForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	_, err := searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestSearchStockNews_Unauthorized(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")
	resetExaCacheForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	_, err := searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestSearchStockNews_MissingAPIKey(t *testing.T) {
	t.Setenv("EXA_API_KEY", "")

	_, err := searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "EXA_API_KEY") {
		t.Fatalf("expected error about EXA_API_KEY, got %q", err.Error())
	}
}

func TestSearchStockNews_ContextCanceled(t *testing.T) {
	t.Setenv("EXA_API_KEY", "test-key")
	resetExaCacheForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is canceled.
		<-r.Context().Done()
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := searchStockNews(ctx, testSymbolAAPL, nil)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestBuildStockSearchQuery(t *testing.T) {
	tests := []struct {
		name    string
		symbol  string
		profile *CompanyProfile
		want    string
	}{
		{
			name:   "with profile name",
			symbol: testSymbolAAPL,
			profile: &CompanyProfile{
				Name: "Apple Inc",
			},
			want: "Apple Inc (AAPL) stock latest news earnings financial performance",
		},
		{
			name:    "nil profile",
			symbol:  "MSFT",
			profile: nil,
			want:    "MSFT stock latest news earnings financial performance",
		},
		{
			name:   "profile with empty name",
			symbol: "GOOGL",
			profile: &CompanyProfile{
				Name: "",
			},
			want: "GOOGL stock latest news earnings financial performance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildStockSearchQuery(tt.symbol, tt.profile)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeExaResults_StripsInvalidUTF8(t *testing.T) {
	results := []exaSearchResult{
		{
			Title:      "Valid Title",
			Highlights: []string{"text with " + string([]byte{0xff, 0xfe}) + " invalid utf8"},
		},
	}

	got := sanitizeExaResults(results)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !utf8.ValidString(got[0].Highlights[0]) {
		t.Fatal("expected valid UTF-8 in highlights after sanitization")
	}
	if !strings.Contains(got[0].Highlights[0], "\ufffd") {
		t.Fatal("expected replacement character U+FFFD for invalid UTF-8")
	}
}

func TestSanitizeExaResults_TruncatesHighlights(t *testing.T) {
	longText := strings.Repeat("a", maxHighlightRuneLen+50)
	results := []exaSearchResult{
		{
			Title:      "Title",
			Highlights: []string{longText},
		},
	}

	got := sanitizeExaResults(results)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if utf8.RuneCountInString(got[0].Highlights[0]) > maxHighlightRuneLen {
		t.Fatalf("highlight not truncated: rune count %d > max %d",
			utf8.RuneCountInString(got[0].Highlights[0]), maxHighlightRuneLen)
	}
}

func TestSanitizeExaResults_DropsEmptyResults(t *testing.T) {
	results := []exaSearchResult{
		{Title: "", Highlights: []string{}},
		{Title: "Valid", Highlights: nil},
		{Title: "", Highlights: []string{"has highlight"}},
	}

	got := sanitizeExaResults(results)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	if got[0].Title != "Valid" {
		t.Errorf("expected 'Valid' title first, got %q", got[0].Title)
	}
}

func TestSanitizeExaResults_NulByteStripped(t *testing.T) {
	results := []exaSearchResult{
		{
			Title:      "Ti\x00tle",
			Highlights: []string{"highlight\x00with\x00nuls"},
		},
	}

	got := sanitizeExaResults(results)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if strings.Contains(got[0].Title, "\x00") {
		t.Fatal("title still contains NUL byte")
	}
	if strings.Contains(got[0].Highlights[0], "\x00") {
		t.Fatal("highlights still contain NUL byte")
	}
}

func TestSanitizeExaResults_TruncatesTitle(t *testing.T) {
	longTitle := strings.Repeat("t", maxTitleRuneLen+20)
	results := []exaSearchResult{
		{Title: longTitle},
	}

	got := sanitizeExaResults(results)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if utf8.RuneCountInString(got[0].Title) > maxTitleRuneLen {
		t.Fatalf("title not truncated: rune count %d > max %d",
			utf8.RuneCountInString(got[0].Title), maxTitleRuneLen)
	}
}

func TestSanitizeExaResults_TruncatesAuthor(t *testing.T) {
	longAuthor := strings.Repeat("a", maxAuthorRuneLen+10)
	results := []exaSearchResult{
		{Title: "Title", Author: longAuthor},
	}

	got := sanitizeExaResults(results)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if utf8.RuneCountInString(got[0].Author) > maxAuthorRuneLen {
		t.Fatalf("author not truncated: rune count %d > max %d",
			utf8.RuneCountInString(got[0].Author), maxAuthorRuneLen)
	}
}

func TestSanitizeExaResults_EmptyHighlightFiltered(t *testing.T) {
	results := []exaSearchResult{
		{
			Title:      "Title",
			Highlights: []string{"", "valid highlight", "   "},
		},
	}

	got := sanitizeExaResults(results)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	// Empty string "" is filtered, "   " (whitespace) is kept (not trimmed
	// by sanitizeForPrompt).
	if len(got[0].Highlights) != 2 {
		t.Fatalf("expected 2 highlights, got %d", len(got[0].Highlights))
	}
	if got[0].Highlights[0] != "valid highlight" {
		t.Errorf("expected 'valid highlight', got %q", got[0].Highlights[0])
	}
}

func TestSanitizeExaResults_MultiByteRuneTruncation(t *testing.T) {
	// 4-byte emoji plus text, truncated at rune boundary.
	title := "🎉" + strings.Repeat("x", maxTitleRuneLen)
	results := []exaSearchResult{
		{Title: title},
	}

	got := sanitizeExaResults(results)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !strings.Contains(got[0].Title, "🎉") {
		t.Fatal("emoji was removed by sanitization")
	}
	if utf8.RuneCountInString(got[0].Title) != maxTitleRuneLen {
		t.Fatalf("expected %d runes, got %d", maxTitleRuneLen, utf8.RuneCountInString(got[0].Title))
	}
}

func TestExaResultsCache_Hit(t *testing.T) {
	// This test mutates the package-level cache — must not use t.Parallel().
	t.Setenv("EXA_API_KEY", "test-key")
	resetExaCacheForTest(t)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exaSearchResponse{
			Results: []exaSearchResult{
				{Title: "Fresh News"},
			},
		})
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	// First call — should hit the server.
	results, err := searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if requestCount != 1 {
		t.Fatalf("expected 1 request on first call, got %d", requestCount)
	}

	// Second call — should return from cache without HTTP request.
	results, err = searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result from cache, got %d", len(results))
	}
	if requestCount != 1 {
		t.Fatalf("expected no additional HTTP request for cached entry, got %d total requests", requestCount)
	}
}

func TestExaResultsCache_Expired(t *testing.T) {
	// This test mutates the package-level cache — must not use t.Parallel().
	t.Setenv("EXA_API_KEY", "test-key")
	t.Setenv("EXA_NUM_RESULTS", "2")
	resetExaCacheForTest(t)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exaSearchResponse{
			Results: []exaSearchResult{
				{Title: "News"},
			},
		})
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	// First call — caches the result.
	_, err := searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Manually expire the cache entry.
	query := buildStockSearchQuery(testSymbolAAPL, nil)
	cacheKey := query + ":2"
	exaCacheMu.Lock()
	if entry, ok := exaCache[cacheKey]; ok {
		entry.expiresAt = time.Now().Add(-1 * time.Minute)
		exaCache[cacheKey] = entry
	}
	exaCacheMu.Unlock()

	// Second call — cache expired, should make a new request.
	_, err = searchStockNews(context.Background(), testSymbolAAPL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests (cache expired), got %d", requestCount)
	}
}

func TestExaResultsCache_Eviction(t *testing.T) {
	// This test mutates the package-level cache — must not use t.Parallel().
	t.Setenv("EXA_API_KEY", "test-key")
	resetExaCacheForTest(t)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exaSearchResponse{
			Results: []exaSearchResult{
				{Title: "News"},
			},
		})
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	// Fill cache beyond max entries.
	for i := range exaCacheMaxEntries + 5 {
		symbol := "S" + strings.Repeat("x", i%3) + string(rune('A'+i%26))
		_, err := searchStockNews(context.Background(), symbol, nil)
		if err != nil {
			t.Fatalf("unexpected error for symbol %s: %v", symbol, err)
		}
	}

	exaCacheMu.Lock()
	cacheLen := len(exaCache)
	exaCacheMu.Unlock()

	if cacheLen > exaCacheMaxEntries {
		t.Fatalf("cache size %d exceeds max %d", cacheLen, exaCacheMaxEntries)
	}
	if cacheLen < 1 {
		t.Fatal("cache should not be empty after filling")
	}
}

func TestLoadExaNumResults(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     int
	}{
		{"default", "", 5},
		{"valid value", "10", 10},
		{"zero returns default", "0", 5},
		{"negative returns default", "-1", 5},
		{"non-numeric returns default", "abc", 5},
		{"capped at 20", "50", 20},
		{"exactly 20", "20", 20},
		{"value 1", "1", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EXA_NUM_RESULTS", tt.envValue)
			got := loadExaNumResults()
			if got != tt.want {
				t.Fatalf("loadExaNumResults() = %d, want %d", got, tt.want)
			}
		})
	}
}
