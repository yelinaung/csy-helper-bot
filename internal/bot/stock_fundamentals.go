package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	dbn "github.com/NimbleMarkets/dbn-go"
	dbn_hist "github.com/NimbleMarkets/dbn-go/hist"
	"github.com/rs/zerolog/log"
)

// financialMetricsResponse is the top-level JSON envelope from
// Finnhub GET /stock/metric?metric=all. Metric fields are nested under
// the "metric" key.
type financialMetricsResponse struct {
	Metric FinancialMetrics `json:"metric"`
}

// FinancialMetrics holds key fundamental ratios from the nested "metric"
// object in Finnhub /stock/metric.
type FinancialMetrics struct {
	//nolint:tagliatelle // Finnhub response uses camelCase.
	PEExclExtraTTM float64 `json:"peBasicExclExtraTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	EPSExclExtraTTM float64 `json:"epsBasicExclExtraItemsTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	RevenuePerShareTTM float64 `json:"revenuePerShareTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	NetProfitMarginTTM float64 `json:"netProfitMarginTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	ROETTM float64 `json:"roeTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	ROATTM float64 `json:"roaTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	DebtToEquityTTM float64 `json:"debtToEquityTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	CurrentRatioTTM float64 `json:"currentRatioTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	BookValuePerShareQ float64 `json:"bookValuePerShareQuarterly"`
	Beta               float64 `json:"beta"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	High52W float64 `json:"52WeekHigh"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	Low52W float64 `json:"52WeekLow"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	DividendYieldIndicated float64 `json:"dividendYieldIndicated"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	RevenueGrowthTTM float64 `json:"revenueGrowthTTM"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	EPSGrowthTTM float64 `json:"epsGrowthTTM"`
	// MarketCapM is redundant with CompanyProfile.MarketCapitalization but
	// comes self-contained in the metrics response for convenience.
	//nolint:tagliatelle // Finnhub response uses camelCase.
	MarketCapM float64 `json:"marketCapitalization"`
}

// EarningsEntry represents one quarterly earnings report from
// Finnhub /stock/earnings.
type EarningsEntry struct {
	Period   string  `json:"period"`
	Actual   float64 `json:"actual"`
	Estimate float64 `json:"estimate"`
	Surprise float64 `json:"surprise"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	SurprisePct float64 `json:"surprisePercent"`
}

// RecommendationTrend holds analyst consensus counts for one period.
// Finnhub /stock/recommendation returns a top-level array sorted by
// period (newest first). fetchRecommendation decodes []RecommendationTrend
// and returns a pointer to the first element, or nil if the array is empty.
type RecommendationTrend struct {
	Period string `json:"period"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	StrongBuy int `json:"strongBuy"`
	Buy       int `json:"buy"`
	Hold      int `json:"hold"`
	Sell      int `json:"sell"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	StrongSell int `json:"strongSell"`
}

// PriceTarget holds analyst price targets from Finnhub /stock/price-target.
type PriceTarget struct {
	//nolint:tagliatelle // Finnhub response uses camelCase.
	LastUpdated string `json:"lastUpdated"`
	Symbol      string `json:"symbol"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	TargetHigh float64 `json:"targetHigh"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	TargetLow float64 `json:"targetLow"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	TargetMean float64 `json:"targetMean"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	TargetMedian float64 `json:"targetMedian"`
	//nolint:tagliatelle // Finnhub response uses camelCase.
	CurrentPrice float64 `json:"lastPrice"`
}

// fetchFinancialMetrics fetches fundamental metrics from Finnhub
// GET /stock/metric?metric=all. Returns nil and an error on failure.
func fetchFinancialMetrics(ctx context.Context, symbol string) (*FinancialMetrics, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
	}
	u, err := url.Parse(finnhubBaseURL + "/stock/metric")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("metric", "all")
	q.Set("token", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(unexpectedCodeErrMsg, resp.StatusCode)
	}

	var wrapper financialMetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, err
	}

	return &wrapper.Metric, nil
}

// fetchEarningsHistory fetches the last 4 quarterly earnings from
// Finnhub GET /stock/earnings?limit=4.
func fetchEarningsHistory(ctx context.Context, symbol string) ([]EarningsEntry, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
	}
	u, err := url.Parse(finnhubBaseURL + "/stock/earnings")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("limit", "4")
	q.Set("token", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(unexpectedCodeErrMsg, resp.StatusCode)
	}

	var entries []EarningsEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}

	return entries, nil
}

// fetchRecommendation returns the most recent analyst consensus, or nil
// if the Finnhub response is an empty array. Internally decodes
// []RecommendationTrend and takes the first element.
func fetchRecommendation(ctx context.Context, symbol string) (*RecommendationTrend, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
	}
	u, err := url.Parse(finnhubBaseURL + "/stock/recommendation")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("token", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(unexpectedCodeErrMsg, resp.StatusCode)
	}

	var trends []RecommendationTrend
	if err := json.NewDecoder(resp.Body).Decode(&trends); err != nil {
		return nil, err
	}

	if len(trends) == 0 {
		//nolint:nilnil // Empty array is a valid "no data" response, not an error.
		return nil, nil
	}

	return &trends[0], nil
}

// fetchPriceTarget fetches analyst price targets from
// Finnhub GET /stock/price-target. Returns nil and an error on failure.
func fetchPriceTarget(ctx context.Context, symbol string) (*PriceTarget, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
	}
	u, err := url.Parse(finnhubBaseURL + "/stock/price-target")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("token", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(unexpectedCodeErrMsg, resp.StatusCode)
	}

	var pt PriceTarget
	if err := json.NewDecoder(resp.Body).Decode(&pt); err != nil {
		return nil, err
	}

	// Finnhub returns all-zero target fields for tickers without analyst
	// coverage. Treat that as no data to avoid consuming prompt budget.
	if pt.TargetMean == 0 && pt.TargetHigh == 0 && pt.TargetLow == 0 && pt.TargetMedian == 0 {
		//nolint:nilnil // No analyst coverage is a valid "no data" response.
		return nil, nil
	}

	return &pt, nil
}

// fetchEarningsReactions fetches a single Databento daily bar window
// covering all earnings periods and computes the next-day-per-period
// percentage move. Finnhub /stock/earnings reports fiscal quarter end
// dates, not announcement dates, so the computed reaction may not reflect
// the actual post-announcement move.
// Returns partial results when Databento is unavailable or when bars
// for a given period cannot be fetched.
//
//nolint:unused // Kept for re-enablement when actual announcement dates are available.
func fetchEarningsReactions(
	ctx context.Context,
	symbol string,
	entries []EarningsEntry,
) []EarningsReaction {
	apiKey := strings.TrimSpace(os.Getenv("DATABENTO_API_KEY"))
	if apiKey == "" {
		return nil
	}

	// Parse periods and find the min/max to do one Databento call.
	type parsedEntry struct {
		entry  EarningsEntry
		period time.Time
	}
	parsed := make([]parsedEntry, len(entries))
	var minPeriod, maxPeriod time.Time
	validCount := 0
	for i, e := range entries {
		parsed[i].entry = e
		p, parseErr := time.Parse(dateFormatPattern, e.Period)
		if parseErr != nil {
			log.Warn().Err(parseErr).Str("period", e.Period).Str("symbol", symbol).
				Msg("Failed to parse earnings period date")
			continue
		}
		parsed[i].period = p
		validCount++
		if minPeriod.IsZero() || p.Before(minPeriod) {
			minPeriod = p
		}
		if p.After(maxPeriod) {
			maxPeriod = p
		}
	}
	if validCount == 0 {
		// All periods failed to parse — return entries with zero reaction.
		reactions := make([]EarningsReaction, 0, len(entries))
		for _, e := range entries {
			reactions = append(reactions, earningsReactionFromEntry(e, 0))
		}
		return reactions
	}

	// Fetch a single window: minPeriod to maxPeriod+5 days.
	bars, err := fetchBarsByDateRange(ctx, symbol, apiKey, minPeriod, maxPeriod.AddDate(0, 0, 5))
	if err != nil {
		log.Warn().Err(err).Str("symbol", symbol).
			Msg("Failed to fetch bars for earnings reactions; returning zero reactions")
		reactions := make([]EarningsReaction, 0, len(entries))
		for _, e := range entries {
			reactions = append(reactions, earningsReactionFromEntry(e, 0))
		}
		return reactions
	}

	// Bucket bars: for each period, find the latest bar on/before the
	// period date (report close) and the first bar after (next-day close).
	reactions := make([]EarningsReaction, 0, len(entries))
	for _, pe := range parsed {
		if pe.period.IsZero() {
			reactions = append(reactions, earningsReactionFromEntry(pe.entry, 0))
			continue
		}
		nextDayPct := 0.0
		var reportClose, nextClose float64
		for _, bar := range bars {
			if !bar.Date.After(pe.period) {
				reportClose = bar.Close // keep latest <= period
			}
			if bar.Date.After(pe.period) && nextClose == 0 {
				nextClose = bar.Close // first bar after period
			}
		}
		if reportClose > 0 && nextClose > 0 {
			nextDayPct = (nextClose/reportClose - 1) * 100
		}
		reactions = append(reactions, earningsReactionFromEntry(pe.entry, nextDayPct))
	}

	return reactions
}

// fetchBarsByDateRange fetches historical bars from Databento for a given
// date range and returns the sorted bars.
//
//nolint:unused // Called only by fetchEarningsReactions (which is also unused).
func fetchBarsByDateRange(
	ctx context.Context,
	symbol, apiKey string,
	start, end time.Time,
) ([]HistoricalBar, error) {
	dataset := strings.TrimSpace(os.Getenv("DATABENTO_DATASET"))
	if dataset == "" {
		dataset = "EQUS.MINI"
	}

	startUTC := start.UTC().Truncate(24 * time.Hour)
	endUTC := end.UTC().Truncate(24 * time.Hour)
	dateRange := dbn_hist.DateRange{Start: startUTC, End: endUTC}

	params := dbn_hist.SubmitJobParams{
		Dataset:     dataset,
		Symbols:     symbol,
		Schema:      dbn.Schema_Ohlcv1D,
		DateRange:   dateRange,
		Encoding:    dbn.Encoding_Dbn,
		Compression: dbn.Compress_None,
		StypeIn:     dbn.SType_RawSymbol,
	}

	raw, err := getHistoricalRangeWithContext(ctx, apiKey, &params)
	if err != nil {
		return nil, err
	}

	records, _, err := dbn.ReadDBNToSlice[dbn.OhlcvMsg](bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}

	bars := make([]HistoricalBar, 0, len(records))
	for _, rec := range records {
		if rec.Header.TsEvent > uint64(math.MaxInt64) {
			return nil, errors.New("invalid timestamp from historical data")
		}
		ts := time.Unix(0, int64(rec.Header.TsEvent)).UTC().Truncate(24 * time.Hour)
		bars = append(bars, HistoricalBar{
			Date:   ts,
			Open:   float64(rec.Open) / 1e9,
			High:   float64(rec.High) / 1e9,
			Low:    float64(rec.Low) / 1e9,
			Close:  float64(rec.Close) / 1e9,
			Volume: rec.Volume,
		})
	}

	slices.SortFunc(bars, func(a, b HistoricalBar) int {
		return a.Date.Compare(b.Date)
	})
	return bars, nil
}

// earningsReactionFromEntry creates an EarningsReaction from an EarningsEntry.
//
//nolint:unused // Private helper for fetchEarningsReactions.
func earningsReactionFromEntry(e EarningsEntry, nextDayChangePct float64) EarningsReaction {
	return EarningsReaction{
		Period:           e.Period,
		Estimate:         e.Estimate,
		Actual:           e.Actual,
		Surprise:         e.Surprise,
		SurprisePct:      e.SurprisePct,
		NextDayChangePct: nextDayChangePct,
	}
}
