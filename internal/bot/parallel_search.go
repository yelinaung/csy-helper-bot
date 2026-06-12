package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultParallelSearchBaseURL = "https://api.parallel.ai/v1/search"

	defaultParallelTimeout    = 15 * time.Second
	defaultParallelMaxResults = 5
	parallelMaxResultsCap     = 10

	maxParallelExcerptRuneLen  = 300
	maxParallelExcerptsPerItem = 3

	// maxParallelErrorBodyBytes bounds how much of an error response is
	// surfaced in errors, keeping quota/validation details without logging
	// large payloads.
	maxParallelErrorBodyBytes = 1024
)

type parallelSearchRequest struct {
	Objective        string                    `json:"objective"`
	SearchQueries    []string                  `json:"search_queries"`
	AdvancedSettings *parallelAdvancedSettings `json:"advanced_settings,omitempty"`
}

type parallelAdvancedSettings struct {
	MaxResults int `json:"max_results,omitempty"`
}

type parallelSearchResult struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	PublishDate string   `json:"publish_date"`
	Excerpts    []string `json:"excerpts"`
}

type parallelSearchResponse struct {
	SearchID string                 `json:"search_id"`
	Results  []parallelSearchResult `json:"results"`
}

// parallelSearcher calls the Parallel.ai Search API. Configuration is
// captured at construction so tests can inject a local server without
// mutating package-level state.
type parallelSearcher struct {
	baseURL    string
	apiKey     string
	timeout    time.Duration
	maxResults int
}

// newParallelSearcher builds a searcher from the environment. It returns nil
// when PARALLEL_API_KEY is not configured, which disables the feature.
func newParallelSearcher() *parallelSearcher {
	apiKey := strings.TrimSpace(os.Getenv("PARALLEL_API_KEY"))
	if apiKey == "" {
		return nil
	}
	return &parallelSearcher{
		baseURL:    defaultParallelSearchBaseURL,
		apiKey:     apiKey,
		timeout:    loadParallelTimeout(),
		maxResults: loadParallelMaxResults(),
	}
}

// search queries the Parallel.ai Search API for fresh web excerpts.
func (p *parallelSearcher) search(ctx context.Context, objective string, queries []string) ([]parallelSearchResult, error) {
	if p == nil {
		return nil, errors.New("parallel searcher not configured")
	}

	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, errors.New("search objective is required")
	}

	reqBody := parallelSearchRequest{
		Objective:        objective,
		SearchQueries:    queries,
		AdvancedSettings: &parallelAdvancedSettings{MaxResults: p.maxResults},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal parallel search request: %w", err)
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultParallelTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, p.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create parallel search request: %w", err)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("parallel search request failed: %w", err)
	}
	// Drain the body before closing so the HTTP client can reuse the
	// underlying connection.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxParallelErrorBodyBytes))
		return nil, fmt.Errorf("parallel search returned status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var searchResp parallelSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("decode parallel search response: %w", err)
	}

	log.Info().
		Str("search_id", searchResp.SearchID).
		Int("result_count", len(searchResp.Results)).
		Msg("Parallel search completed")

	return sanitizeParallelResults(searchResp.Results), nil
}

// sanitizeParallelResults applies per-field sanitization with rune budgets,
// mirroring sanitizeExaResults. Results with empty titles AND empty excerpts
// are dropped. URLs are kept raw.
func sanitizeParallelResults(results []parallelSearchResult) []parallelSearchResult {
	sanitized := make([]parallelSearchResult, 0, len(results))
	for _, r := range results {
		sr := parallelSearchResult{
			URL:         r.URL,
			PublishDate: r.PublishDate,
		}
		sr.Title = sanitizeForPrompt(r.Title, maxTitleRuneLen)
		for _, e := range r.Excerpts {
			if len(sr.Excerpts) >= maxParallelExcerptsPerItem {
				break
			}
			clean := sanitizeForPrompt(e, maxParallelExcerptRuneLen)
			if clean != "" {
				sr.Excerpts = append(sr.Excerpts, clean)
			}
		}
		if sr.Title != "" || len(sr.Excerpts) > 0 {
			sanitized = append(sanitized, sr)
		}
	}
	return sanitized
}

// loadParallelTimeout reads PARALLEL_TIMEOUT_SECONDS, defaulting to 15s on
// missing or invalid values.
func loadParallelTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("PARALLEL_TIMEOUT_SECONDS"))
	if raw == "" {
		return defaultParallelTimeout
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultParallelTimeout
	}
	return time.Duration(seconds) * time.Second
}

// loadParallelMaxResults reads PARALLEL_MAX_RESULTS, defaulting to 5 and
// capping at 10.
func loadParallelMaxResults() int {
	raw := strings.TrimSpace(os.Getenv("PARALLEL_MAX_RESULTS"))
	if raw == "" {
		return defaultParallelMaxResults
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultParallelMaxResults
	}
	return min(n, parallelMaxResultsCap)
}
