package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultParallelExtractBaseURL = "https://api.parallel.ai/v1/extract"

	defaultParallelExtractTimeout = 30 * time.Second
	defaultExtractMaxURLs         = 3
	extractMaxURLsCap             = 10

	maxParallelExtractExcerptRuneLen  = 1500
	maxParallelExtractExcerptsPerItem = 4

	// maxExtractErrorContentRuneLen bounds how much of a per-URL error's
	// content field is logged, keeping diagnostics useful without dumping
	// large error payloads into logs.
	maxExtractErrorContentRuneLen = 300
)

type parallelExtractRequest struct {
	URLs      []string `json:"urls"`
	Objective string   `json:"objective"`
}

type parallelExtractResult struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	PublishDate string   `json:"publish_date"`
	Excerpts    []string `json:"excerpts"`
}

// parallelExtractError matches the documented v1 ExtractError schema
// (https://docs.parallel.ai/api-reference/extract/extract). A response
// struct without this field silently discards per-URL failures via
// encoding/json, defeating the diagnostics it exists for.
type parallelExtractError struct {
	URL            string `json:"url"`
	ErrorType      string `json:"error_type"`
	HTTPStatusCode *int   `json:"http_status_code"`
	Content        string `json:"content"`
}

type parallelExtractResponse struct {
	ExtractID string                  `json:"extract_id"`
	Results   []parallelExtractResult `json:"results"`
	Errors    []parallelExtractError  `json:"errors"`
}

// parallelExtractor calls the Parallel.ai Extract API. Configuration is
// captured at construction so tests can inject a local server without
// mutating package-level state.
type parallelExtractor struct {
	baseURL string
	apiKey  string
	timeout time.Duration
	maxURLs int
}

// newParallelExtractor builds an extractor from the environment. It returns
// nil when PARALLEL_API_KEY is not configured or when EXTRACT_ENABLED is
// explicitly false, either of which disables the feature.
func newParallelExtractor() *parallelExtractor {
	apiKey := strings.TrimSpace(os.Getenv("PARALLEL_API_KEY"))
	if apiKey == "" {
		return nil
	}
	if !loadExtractEnabled() {
		return nil
	}
	return &parallelExtractor{
		baseURL: defaultParallelExtractBaseURL,
		apiKey:  apiKey,
		timeout: loadExtractTimeout(),
		maxURLs: loadExtractMaxURLs(),
	}
}

// extract calls the Parallel Extract API for the given URLs, focused on the
// given objective. Per-URL failures reported in the response are logged and
// skipped; only a transport-level or non-200 failure returns an error.
func (p *parallelExtractor) extract(ctx context.Context, urls []string, objective string) (results []parallelExtractResult, err error) {
	if p == nil {
		return nil, errors.New("parallel extractor not configured")
	}

	ctx, span := tracer().Start(
		ctx, "parallel.extract",
		trace.WithAttributes(
			attribute.Int("parallel.urls_count", len(urls)),
			// Record only the objective length, not the raw text: the
			// objective can fall back to the user's question (see
			// answerTextQuestion), so recording it verbatim would leak PII
			// into exported traces.
			attribute.Int("parallel.objective_len", len(strings.TrimSpace(objective))),
		),
	)
	defer func() {
		recordSpanError(span, err)
		span.End()
	}()

	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, errors.New("extract objective is required")
	}
	if len(urls) == 0 {
		return nil, errors.New("at least one URL is required")
	}

	reqBody := parallelExtractRequest{URLs: urls, Objective: objective}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal parallel extract request: %w", err)
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultParallelExtractTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, p.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create parallel extract request: %w", err)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := parallelHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("parallel extract request failed: %w", err)
	}
	// Drain the body before closing so the HTTP client can reuse the
	// underlying connection.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxParallelErrorBodyBytes))
		return nil, fmt.Errorf("parallel extract returned status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var extractResp parallelExtractResponse
	if err := json.NewDecoder(resp.Body).Decode(&extractResp); err != nil {
		return nil, fmt.Errorf("decode parallel extract response: %w", err)
	}

	for _, extractErr := range extractResp.Errors {
		logExtractURLError(extractErr)
	}

	sanitized := sanitizeParallelExtractResults(extractResp.Results)

	span.SetAttributes(attribute.Int("parallel.excerpts_count", countExtractExcerpts(sanitized)))

	log.Info().
		Str("extract_id", extractResp.ExtractID).
		Int("result_count", len(sanitized)).
		Int("error_count", len(extractResp.Errors)).
		Msg("Parallel extract completed")

	return sanitized, nil
}

// logExtractURLError logs a single per-URL failure without leaking the raw
// URL (which can carry PII in its path/query string) into logs.
func logExtractURLError(extractErr parallelExtractError) {
	statusCode := -1
	if extractErr.HTTPStatusCode != nil {
		statusCode = *extractErr.HTTPStatusCode
	}
	log.Warn().
		Str("host", urlHost(extractErr.URL)).
		Str("error_type", extractErr.ErrorType).
		Int("http_status_code", statusCode).
		Str("content", sanitizeForPrompt(extractErr.Content, maxExtractErrorContentRuneLen)).
		Msg("Parallel extract failed for URL")
}

// urlHost extracts just the host from a URL for low-cardinality, low-PII
// logging. Returns "" for unparsable input rather than logging the raw URL.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func countExtractExcerpts(results []parallelExtractResult) int {
	count := 0
	for _, r := range results {
		count += len(r.Excerpts)
	}
	return count
}

// sanitizeParallelExtractResults applies per-field sanitization with rune
// budgets, mirroring sanitizeParallelResults, but with a stricter keep rule:
// a result must have at least one non-empty excerpt after sanitization, or
// it is dropped. Title alone is not enough here (unlike search), because the
// grounded explainer's entire premise is page content; a title-only result
// would count as a successful extraction and invoke the explainer with
// nothing to answer from, bypassing the zero-excerpt fallback.
func sanitizeParallelExtractResults(results []parallelExtractResult) []parallelExtractResult {
	sanitized := make([]parallelExtractResult, 0, len(results))
	for _, r := range results {
		sr := parallelExtractResult{
			URL:         r.URL,
			PublishDate: r.PublishDate,
		}
		sr.Title = sanitizeForPrompt(r.Title, maxTitleRuneLen)
		for _, e := range r.Excerpts {
			if len(sr.Excerpts) >= maxParallelExtractExcerptsPerItem {
				break
			}
			clean := sanitizeForPrompt(e, maxParallelExtractExcerptRuneLen)
			if clean != "" {
				sr.Excerpts = append(sr.Excerpts, clean)
			}
		}
		if len(sr.Excerpts) > 0 {
			sanitized = append(sanitized, sr)
		}
	}
	return sanitized
}

// loadExtractEnabled reads EXTRACT_ENABLED, the extraction kill switch.
// Missing or unparsable values default to enabled so the feature activates
// off PARALLEL_API_KEY alone unless explicitly turned off.
func loadExtractEnabled() bool {
	raw := getenvTrim("EXTRACT_ENABLED")
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		log.Warn().Str("value", raw).Msg("Invalid EXTRACT_ENABLED value; defaulting to enabled")
		return true
	}
	return enabled
}

// loadExtractTimeout reads EXTRACT_TIMEOUT_SECONDS, defaulting to 30s on
// missing or invalid values. Page rendering is slower than a search query,
// hence the longer default than PARALLEL_TIMEOUT_SECONDS.
func loadExtractTimeout() time.Duration {
	raw := getenvTrim("EXTRACT_TIMEOUT_SECONDS")
	if raw == "" {
		return defaultParallelExtractTimeout
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultParallelExtractTimeout
	}
	return time.Duration(seconds) * time.Second
}

// loadExtractMaxURLs reads EXTRACT_MAX_URLS, defaulting to 3 and clamping to
// the Extract API's 10-URL-per-request limit. This is a real knob: it drives
// both the URL detection cap in extractQuestionURLs and, transitively, the
// per-question excerpt budget sent to the explainer.
func loadExtractMaxURLs() int {
	raw := getenvTrim("EXTRACT_MAX_URLS")
	if raw == "" {
		return defaultExtractMaxURLs
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultExtractMaxURLs
	}
	return min(n, extractMaxURLsCap)
}
