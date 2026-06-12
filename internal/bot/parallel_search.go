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
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultParallelTimeout    = 15 * time.Second
	defaultParallelMaxResults = 5
	parallelMaxResultsCap     = 10

	maxParallelExcerptRuneLen  = 300
	maxParallelExcerptsPerItem = 3
)

// parallelSearchBaseURL is a variable so tests can point it at a local server.
var parallelSearchBaseURL = "https://api.parallel.ai/v1/search"

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

func parallelSearchEnabled() bool {
	return strings.TrimSpace(os.Getenv("PARALLEL_API_KEY")) != ""
}

// searchParallel queries the Parallel.ai Search API for fresh web excerpts.
// The PARALLEL_API_KEY environment variable must be set.
func searchParallel(ctx context.Context, objective string, queries []string) ([]parallelSearchResult, error) {
	apiKey := strings.TrimSpace(os.Getenv("PARALLEL_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("PARALLEL_API_KEY not configured")
	}

	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, errors.New("search objective is required")
	}

	reqBody := parallelSearchRequest{
		Objective:        objective,
		SearchQueries:    queries,
		AdvancedSettings: &parallelAdvancedSettings{MaxResults: loadParallelMaxResults()},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal parallel search request: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, loadParallelTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, parallelSearchBaseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create parallel search request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("parallel search request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("parallel search returned status %d", resp.StatusCode)
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
