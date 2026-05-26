package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
