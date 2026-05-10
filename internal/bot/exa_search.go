package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

var (
	exaCacheMu sync.Mutex
	exaCache   = map[string]cachedExaResults{}
)

const (
	exaCacheTTL        = 5 * time.Minute
	exaCacheMaxEntries = 100

	maxHighlightRuneLen = 200
	maxTitleRuneLen     = 150
	maxAuthorRuneLen    = 100
)

type exaSearchRequest struct {
	Query              string `json:"query"`
	Type               string `json:"type"`
	Category           string `json:"category"`
	StartPublishedDate string `json:"startPublishedDate"` //nolint:tagliatelle // Exa API uses camelCase.
	NumResults         int    `json:"numResults"`         //nolint:tagliatelle // Exa API uses camelCase.
	Contents           struct {
		Highlights bool `json:"highlights"`
	} `json:"contents"`
}

type exaSearchResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	PublishedDate string   `json:"publishedDate"` //nolint:tagliatelle // Exa API uses camelCase.
	Author        string   `json:"author"`
	Highlights    []string `json:"highlights"`
}

type exaSearchResponse struct {
	RequestID   string            `json:"requestId"` //nolint:tagliatelle // Exa API uses camelCase.
	Results     []exaSearchResult `json:"results"`
	CostDollars struct {
		Total float64 `json:"total"`
	} `json:"costDollars"` //nolint:tagliatelle // Exa API uses camelCase.
}

type cachedExaResults struct {
	results   []exaSearchResult
	expiresAt time.Time
}

// searchStockNews queries the Exa API for recent news about a stock and
// returns sanitized results. Results are cached for exaCacheTTL. The
// EXA_API_KEY environment variable must be set.
func searchStockNews(ctx context.Context, symbol string, profile *CompanyProfile) ([]exaSearchResult, error) {
	apiKey := strings.TrimSpace(os.Getenv("EXA_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("EXA_API_KEY not configured")
	}

	numResults := loadExaNumResults()
	query := buildStockSearchQuery(symbol, profile)
	cacheKey := query + ":" + strconv.Itoa(numResults)

	exaCacheMu.Lock()
	if cached, ok := exaCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		exaCacheMu.Unlock()
		return cached.results, nil
	}
	exaCacheMu.Unlock()

	startDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	reqBody := exaSearchRequest{
		Query:              query,
		Type:               "auto",
		Category:           "news",
		StartPublishedDate: startDate,
		NumResults:         numResults,
	}
	reqBody.Contents.Highlights = true

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal exa search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.exa.ai/search", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create exa request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req = req.WithContext(timeoutCtx)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exa search request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exa search returned status %d", resp.StatusCode)
	}

	var searchResp exaSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("decode exa search response: %w", err)
	}

	log.Info().
		Str("symbol", symbol).
		Float64("cost_dollars", searchResp.CostDollars.Total).
		Int("result_count", len(searchResp.Results)).
		Msg("Exa search completed")

	results := sanitizeExaResults(searchResp.Results)

	exaCacheMu.Lock()
	if len(exaCache) >= exaCacheMaxEntries {
		var oldestKey string
		oldestTime := time.Now()
		for k, v := range exaCache {
			if oldestKey == "" || v.expiresAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.expiresAt
			}
		}
		delete(exaCache, oldestKey)
	}
	exaCache[cacheKey] = cachedExaResults{
		results:   results,
		expiresAt: time.Now().Add(exaCacheTTL),
	}
	exaCacheMu.Unlock()

	return results, nil
}

// buildStockSearchQuery constructs a search query string for Exa using
// the company profile name when available.
func buildStockSearchQuery(symbol string, profile *CompanyProfile) string {
	if profile != nil && profile.Name != "" {
		return fmt.Sprintf("%s (%s) stock latest news earnings financial performance", profile.Name, symbol)
	}
	return symbol + " stock latest news earnings financial performance"
}

// sanitizeExaResults applies per-field sanitization to Exa search results
// using rune budgets. Results with empty titles AND empty highlights are
// dropped. URLs are kept raw.
func sanitizeExaResults(results []exaSearchResult) []exaSearchResult {
	sanitized := make([]exaSearchResult, 0, len(results))
	for _, r := range results {
		sr := exaSearchResult{
			PublishedDate: r.PublishedDate,
		}
		sr.Title = sanitizeForPrompt(r.Title, maxTitleRuneLen)
		sr.Author = sanitizeForPrompt(r.Author, maxAuthorRuneLen)
		sr.URL = r.URL
		for _, h := range r.Highlights {
			clean := sanitizeForPrompt(h, maxHighlightRuneLen)
			if clean != "" {
				sr.Highlights = append(sr.Highlights, clean)
			}
		}
		if sr.Title != "" || len(sr.Highlights) > 0 {
			sanitized = append(sanitized, sr)
		}
	}
	return sanitized
}

// loadExaNumResults reads the EXA_NUM_RESULTS environment variable,
// defaulting to 5 and capping at 20.
func loadExaNumResults() int {
	raw := strings.TrimSpace(os.Getenv("EXA_NUM_RESULTS"))
	if raw == "" {
		return 5
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 5
	}
	if n > 20 {
		return 20
	}
	return n
}
