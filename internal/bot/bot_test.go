package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	dbn_hist "github.com/NimbleMarkets/dbn-go/hist"
	"github.com/go-telegram/bot/models"
)

const (
	testSymbolAAPL         = "AAPL"
	testErrInvalidUsage    = "invalid usage"
	testProfileName        = "Apple Inc"
	testStockCommand       = "!sa AAPL"
	testIndustryTechnology = "Technology"

	// Finnhub API endpoint paths used across test fixtures.
	finnhubQuotePath       = "/api/v1/quote"
	finnhubProfilePath     = "/api/v1/stock/profile2"
	finnhubMetricsPath     = "/api/v1/stock/metric"
	finnhubEarningsPath    = "/api/v1/stock/earnings"
	finnhubRecommendPath   = "/api/v1/stock/recommendation"
	finnhubPriceTargetPath = "/api/v1/stock/price-target"

	// Shared earnings mock data period to reduce goconst.
	testEarningsPeriod1 = "2026-03-31"
	testRecommendPeriod = "2026-05"
)

type rewriteHostTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	cloneURL := *req.URL
	clone.URL = &cloneURL
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

func useRedirectedHTTPClient(t *testing.T, serverURL string) {
	t.Helper()

	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("invalid test server url %q: %v", serverURL, err)
	}

	orig := httpClient
	baseTransport := http.DefaultTransport
	if orig != nil && orig.Transport != nil {
		baseTransport = orig.Transport
	}
	httpClient = &http.Client{
		Timeout: orig.Timeout,
		Transport: &rewriteHostTransport{
			base:   baseTransport,
			target: target,
		},
	}

	t.Cleanup(func() {
		httpClient = orig
	})
}

func TestFetchDailyLeetCode(t *testing.T) {
	mockResponse := graphQLResponse{}
	mockResponse.Data.ActiveDailyCodingChallengeQuestion.Question.Title = "Two Sum"
	mockResponse.Data.ActiveDailyCodingChallengeQuestion.Question.TitleSlug = "two-sum"
	mockResponse.Data.ActiveDailyCodingChallengeQuestion.Question.Difficulty = "Easy"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	question, err := fetchDailyLeetCode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if question.Title != "Two Sum" {
		t.Errorf("expected title 'Two Sum', got '%s'", question.Title)
	}
	if question.TitleSlug != "two-sum" {
		t.Errorf("expected titleSlug 'two-sum', got '%s'", question.TitleSlug)
	}
	if question.Difficulty != "Easy" {
		t.Errorf("expected difficulty 'Easy', got '%s'", question.Difficulty)
	}
}

func TestFetchDailyLeetCode_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	_, err := fetchDailyLeetCode(context.Background())
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestFetchDailyLeetCode_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	_, err := fetchDailyLeetCode(context.Background())
	if err == nil {
		t.Error("expected error for invalid JSON response")
	}
}

func TestFetchDailyLeetCode_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"rate limited"}]}`))
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	_, err := fetchDailyLeetCode(context.Background())
	if err == nil {
		t.Error("expected error for graphql errors response")
	}
}

func TestFetchDailyLeetCode_EmptyQuestionData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"activeDailyCodingChallengeQuestion":{"question":{"title":"","titleSlug":"","difficulty":""}}}}`))
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)

	_, err := fetchDailyLeetCode(context.Background())
	if err == nil {
		t.Error("expected error for empty question data")
	}
}

func TestFormatLeetCodeMessage(t *testing.T) {
	tests := []struct {
		name      string
		question  LeetCodeQuestion
		wantEmoji string
	}{
		{
			name:      "Easy question",
			question:  LeetCodeQuestion{Title: "Two Sum", TitleSlug: "two-sum", Difficulty: "Easy"},
			wantEmoji: "🟩",
		},
		{
			name:      "Medium question",
			question:  LeetCodeQuestion{Title: "Add Two Numbers", TitleSlug: "add-two-numbers", Difficulty: "Medium"},
			wantEmoji: "🟨",
		},
		{
			name:      "Hard question",
			question:  LeetCodeQuestion{Title: "Median of Two Sorted Arrays", TitleSlug: "median-of-two-sorted-arrays", Difficulty: "Hard"},
			wantEmoji: "🟥",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := formatLeetCodeMessage(&tt.question)
			if msg == "" {
				t.Error("expected non-empty message")
			}
			if !strings.Contains(msg, tt.question.Title) {
				t.Errorf("message should contain title '%s'", tt.question.Title)
			}
			if !strings.Contains(msg, tt.wantEmoji) {
				t.Errorf("message should contain emoji '%s'", tt.wantEmoji)
			}
			if !strings.Contains(msg, tt.question.TitleSlug) {
				t.Errorf("message should contain URL with slug '%s'", tt.question.TitleSlug)
			}
		})
	}
}

func TestFormatLeetCodeMessage_ContainsURL(t *testing.T) {
	question := LeetCodeQuestion{
		Title:      "Two Sum",
		TitleSlug:  "two-sum",
		Difficulty: "Easy",
	}

	msg := formatLeetCodeMessage(&question)

	expectedURL := "https://leetcode.com/problems/two-sum/"
	if !strings.Contains(msg, expectedURL) {
		t.Errorf("message should contain URL '%s', got '%s'", expectedURL, msg)
	}
}

func TestFormatLeetCodeMessage_ContainsDate(t *testing.T) {
	question := LeetCodeQuestion{
		Title:      "Two Sum",
		TitleSlug:  "two-sum",
		Difficulty: "Easy",
	}

	msg := formatLeetCodeMessage(&question)

	if !strings.Contains(msg, "Date:") {
		t.Error("message should contain 'Date:'")
	}
}

func TestFormatLeetCodeMessage_UnknownDifficulty(t *testing.T) {
	question := LeetCodeQuestion{
		Title:      "Unknown",
		TitleSlug:  "unknown",
		Difficulty: "Unknown",
	}

	msg := formatLeetCodeMessage(&question)

	if msg == "" {
		t.Error("should still generate message for unknown difficulty")
	}
	if !strings.Contains(msg, "Unknown") {
		t.Error("message should contain difficulty text even if no emoji")
	}
}

func TestFormatStockMessage_PositiveChange(t *testing.T) {
	quote := &StockQuote{
		CurrentPrice:  150.25,
		Change:        2.50,
		PercentChange: 1.69,
		High:          151.00,
		Low:           148.50,
		Open:          149.00,
		PreviousClose: 147.75,
	}
	profile := &CompanyProfile{
		Name:                 testProfileName,
		MarketCapitalization: 3000000,
		Industry:             testIndustryTechnology,
	}

	msg := formatStockMessage(testSymbolAAPL, quote, profile)

	if !strings.Contains(msg, testSymbolAAPL) {
		t.Error("message should contain symbol")
	}
	if !strings.Contains(msg, testProfileName) {
		t.Error("message should contain company name")
	}
	if !strings.Contains(msg, "🟢") {
		t.Error("message should contain green emoji for positive change")
	}
	if !strings.Contains(msg, "150.25") {
		t.Error("message should contain current price")
	}
	if !strings.Contains(msg, "Market Cap") {
		t.Error("message should contain market cap")
	}
	if !strings.Contains(msg, "Technology") {
		t.Error("message should contain industry")
	}
}

func TestFormatStockMessage_NegativeChange(t *testing.T) {
	quote := &StockQuote{
		CurrentPrice:  145.00,
		Change:        -3.50,
		PercentChange: -2.36,
		High:          149.00,
		Low:           144.50,
		Open:          148.00,
		PreviousClose: 148.50,
	}

	msg := formatStockMessage("MSFT", quote, nil)

	if !strings.Contains(msg, "🔴") {
		t.Error("message should contain red emoji for negative change")
	}
	if !strings.Contains(msg, "-3.50") {
		t.Error("message should contain negative change value")
	}
}

func TestFormatStockMessage_ContainsAllFields(t *testing.T) {
	quote := &StockQuote{
		CurrentPrice:  100.00,
		Change:        0.00,
		PercentChange: 0.00,
		High:          101.00,
		Low:           99.00,
		Open:          100.50,
		PreviousClose: 100.00,
	}

	msg := formatStockMessage("TEST", quote, nil)

	if !strings.Contains(msg, "Current:") {
		t.Error("message should contain Current label")
	}
	if !strings.Contains(msg, "Change:") {
		t.Error("message should contain Change label")
	}
	if !strings.Contains(msg, "Open:") {
		t.Error("message should contain Open label")
	}
	if !strings.Contains(msg, "High:") {
		t.Error("message should contain High label")
	}
	if !strings.Contains(msg, "Low:") {
		t.Error("message should contain Low label")
	}
	if !strings.Contains(msg, "Previous Close:") {
		t.Error("message should contain Previous Close label")
	}
}

func TestFetchStockQuote(t *testing.T) {
	mockQuote := StockQuote{
		CurrentPrice:  150.25,
		Change:        2.50,
		PercentChange: 1.69,
		High:          151.00,
		Low:           148.50,
		Open:          149.00,
		PreviousClose: 147.75,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != testSymbolAAPL {
			t.Errorf("expected symbol=AAPL, got %s", r.URL.Query().Get("symbol"))
		}
		if r.URL.Query().Get("token") != "test-key" {
			t.Errorf("expected token=test-key, got %s", r.URL.Query().Get("token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockQuote)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchStockQuote(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.CurrentPrice != 150.25 {
		t.Errorf("expected price 150.25, got %f", result.CurrentPrice)
	}
	if result.Change != 2.50 {
		t.Errorf("expected change 2.50, got %f", result.Change)
	}
}

func TestFetchStockQuote_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	_, err := fetchStockQuote(context.Background(), testSymbolAAPL)
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestFetchStockQuote_SymbolNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(StockQuote{})
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	_, err := fetchStockQuote(context.Background(), "INVALID")
	if err == nil {
		t.Error("expected error for symbol not found")
	}
}

func TestFetchCompanyProfile(t *testing.T) {
	mockProfile := CompanyProfile{
		Name:                 testProfileName,
		MarketCapitalization: 3000000,
		Industry:             testIndustryTechnology,
		Exchange:             "NASDAQ",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != testSymbolAAPL {
			t.Errorf("expected symbol=AAPL, got %s", r.URL.Query().Get("symbol"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockProfile)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchCompanyProfile(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Name != testProfileName {
		t.Errorf("expected name 'Apple Inc', got '%s'", result.Name)
	}
	if result.Industry != "Technology" {
		t.Errorf("expected industry 'Technology', got '%s'", result.Industry)
	}
}

func TestBlockedStockResponse(t *testing.T) {
	orig := blockedStocks
	defer func() { blockedStocks = orig }()

	blockedStocks = map[string]string{
		"TEAM": "Please.. no.. don't .. oh god why",
		"SCAM": "nope",
	}

	tests := []struct {
		name        string
		symbol      string
		wantBlocked bool
		wantMsg     string
	}{
		{"blocked symbol returns message", "TEAM", true, "Please.. no.. don't .. oh god why"},
		{"another blocked symbol", "SCAM", true, "nope"},
		{"unblocked symbol", testSymbolAAPL, false, ""},
		{"lowercase not matched", "team", false, ""},
		{"empty symbol", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, blocked := blockedStockResponse(tt.symbol)
			if blocked != tt.wantBlocked {
				t.Errorf("blockedStockResponse(%q) blocked = %v, want %v", tt.symbol, blocked, tt.wantBlocked)
			}
			if msg != tt.wantMsg {
				t.Errorf("blockedStockResponse(%q) msg = %q, want %q", tt.symbol, msg, tt.wantMsg)
			}
		})
	}
}

func TestBlockedStockResponse_EmptyMap(t *testing.T) {
	orig := blockedStocks
	defer func() { blockedStocks = orig }()

	blockedStocks = map[string]string{}

	msg, blocked := blockedStockResponse("TEAM")
	if blocked {
		t.Errorf("expected TEAM to not be blocked when map is empty")
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestParseStockCommand(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantSym   string
		wantDays  int
		wantError bool
		errSubstr string
	}{
		{name: "spot quote", input: "!s AAPL", wantSym: testSymbolAAPL, wantDays: 0},
		{name: "historical 7d", input: "!s AAPL 7d", wantSym: testSymbolAAPL, wantDays: 7},
		{name: "historical 30d", input: "!s msft 30d", wantSym: "MSFT", wantDays: 30},
		{name: "tab after command", input: "!s\tAAPL", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "newline after command with range", input: "!s\nAAPL 7d", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "missing separator after command", input: "!sAAPL", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "invalid range", input: "!s AAPL 10d", wantError: true, errSubstr: "invalid range"},
		{name: "invalid range 1d", input: "!s AAPL 1d", wantError: true, errSubstr: "invalid range"},
		{name: "invalid range 365d", input: "!s AAPL 365d", wantError: true, errSubstr: "invalid range"},
		{name: "invalid symbol chars", input: "!s $$$", wantError: true, errSubstr: "invalid stock symbol"},
		{name: "invalid symbol punctuation", input: "!s AAPL!", wantError: true, errSubstr: "invalid stock symbol"},
		{name: "invalid symbol with extra token", input: "!s AA PL", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "second symbol token", input: "!s AAPL MSFT", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "nonsensical second token", input: "!s AAPL foobar", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "empty", input: "!s", wantError: true, errSubstr: "please provide"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSym, gotDays, err := parseStockCommand(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got symbol=%q days=%d", gotSym, gotDays)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotSym != tt.wantSym || gotDays != tt.wantDays {
				t.Fatalf("got (%q,%d), want (%q,%d)", gotSym, gotDays, tt.wantSym, tt.wantDays)
			}
		})
	}
}

func TestRenderHistoricalChartPNG(t *testing.T) {
	bars := []HistoricalBar{
		{Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Close: 100},
		{Date: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), Close: 101.25},
		{Date: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), Close: 99.75},
	}
	buf, err := renderHistoricalChartPNG(testSymbolAAPL, 7, bars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(buf) == 0 {
		t.Fatal("expected non-empty PNG bytes")
	}
	if len(buf) < 8 {
		t.Fatalf("expected PNG bytes length >= 8, got %d", len(buf))
	}
	wantSig := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	if !bytes.Equal(buf[:8], wantSig) {
		t.Fatalf("invalid PNG signature: got %v want %v", buf[:8], wantSig)
	}
}

func TestFormatHistoricalSummary(t *testing.T) {
	bars := []HistoricalBar{
		{Date: time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), Close: 100, High: 102, Low: 99},
		{Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Close: 110, High: 111, Low: 98},
	}
	got := formatHistoricalSummary(testSymbolAAPL, 7, bars, nil)
	if !strings.Contains(got, "AAPL 7d") {
		t.Fatalf("expected symbol and range in summary, got %q", got)
	}
	if !strings.Contains(got, "Return: 10.00%") {
		t.Fatalf("expected computed return in summary, got %q", got)
	}
}

func TestFormatHistoricalSummary_EmptyBars(t *testing.T) {
	got := formatHistoricalSummary(testSymbolAAPL, 7, nil, nil)
	if !strings.Contains(got, "No historical data returned") {
		t.Fatalf("expected empty-data message, got %q", got)
	}
}

func TestFormatHistoricalSummary_WithProfile(t *testing.T) {
	bars := []HistoricalBar{
		{Date: time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), Close: 100, High: 102, Low: 99},
		{Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Close: 110, High: 111, Low: 98},
	}
	profile := &CompanyProfile{
		Name:                 "Microsoft Corporation",
		MarketCapitalization: 439620,
		Industry:             testIndustryTechnology,
	}

	got := formatHistoricalSummary("MSFT", 7, bars, profile)
	if !strings.Contains(got, "Microsoft Corporation (MSFT)") {
		t.Fatalf("expected company title in summary, got %q", got)
	}
	if !strings.Contains(got, "🏢 Market Cap: $439.62B") {
		t.Fatalf("expected market cap in summary, got %q", got)
	}
	if !strings.Contains(got, "🏭 Industry: Technology") {
		t.Fatalf("expected industry in summary, got %q", got)
	}
}

func TestHistoricalDateRangeUTC(t *testing.T) {
	now := time.Date(2026, 3, 8, 15, 4, 5, 0, time.FixedZone("UTC+8", 8*3600))
	got := historicalDateRangeUTC(now, 7)
	wantEnd := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)

	if !got.End.Equal(wantEnd) {
		t.Fatalf("end mismatch: got %s want %s", got.End, wantEnd)
	}
	if !got.Start.Equal(wantStart) {
		t.Fatalf("start mismatch: got %s want %s", got.Start, wantStart)
	}
}

func TestTryAdjustRangeFromDatabento422(t *testing.T) {
	orig := dbn_hist.SubmitJobParams{
		DateRange: dbn_hist.DateRange{
			Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
		},
	}
	err := &httpStatusError{
		StatusCode: http.StatusUnprocessableEntity,
		Status:     "422 Unprocessable Entity",
		Body:       `{"detail":{"case":"data_end_after_available_end","payload":{"available_end":"2026-03-07T00:00:00.000000000Z"}}}`,
	}

	adjusted, ok := tryAdjustRangeFromDatabento422(&orig, err, 7)
	if !ok {
		t.Fatal("expected adjustment for data_end_after_available_end")
	}
	wantEnd := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if !adjusted.DateRange.End.Equal(wantEnd) {
		t.Fatalf("end mismatch: got %s want %s", adjusted.DateRange.End, wantEnd)
	}
	if !adjusted.DateRange.Start.Equal(wantStart) {
		t.Fatalf("start mismatch: got %s want %s", adjusted.DateRange.Start, wantStart)
	}
}

func TestTryAdjustRangeFromDatabento422_SchemaNotFullyAvailable(t *testing.T) {
	orig := dbn_hist.SubmitJobParams{
		DateRange: dbn_hist.DateRange{
			Start: time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		},
	}
	err := &httpStatusError{
		StatusCode: http.StatusUnprocessableEntity,
		Status:     "422 Unprocessable Entity",
		Body:       `{"detail":{"case":"data_schema_not_fully_available","payload":{"available_start":"2023-03-28T00:00:00.000000000Z","available_end":"2026-03-07T00:00:00.000000000Z"}}}`,
	}

	adjusted, ok := tryAdjustRangeFromDatabento422(&orig, err, 30)
	if !ok {
		t.Fatal("expected adjustment for data_schema_not_fully_available")
	}
	wantEnd := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC)
	if !adjusted.DateRange.End.Equal(wantEnd) {
		t.Fatalf("end mismatch: got %s want %s", adjusted.DateRange.End, wantEnd)
	}
	if !adjusted.DateRange.Start.Equal(wantStart) {
		t.Fatalf("start mismatch: got %s want %s", adjusted.DateRange.Start, wantStart)
	}
}

func TestTryAdjustRangeFromDatabento422_ClampsToAvailableStart(t *testing.T) {
	orig := dbn_hist.SubmitJobParams{
		DateRange: dbn_hist.DateRange{
			Start: time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2023, 4, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	err := &httpStatusError{
		StatusCode: http.StatusUnprocessableEntity,
		Status:     "422 Unprocessable Entity",
		Body:       `{"detail":{"case":"data_schema_not_fully_available","payload":{"available_start":"2023-03-28T00:00:00.000000000Z","available_end":"2023-04-01T00:00:00.000000000Z"}}}`,
	}

	adjusted, ok := tryAdjustRangeFromDatabento422(&orig, err, 30)
	if !ok {
		t.Fatal("expected adjustment with available_start clamp")
	}
	wantStart := time.Date(2023, 3, 28, 0, 0, 0, 0, time.UTC)
	if !adjusted.DateRange.Start.Equal(wantStart) {
		t.Fatalf("start mismatch: got %s want %s", adjusted.DateRange.Start, wantStart)
	}
}

func TestTryAdjustRangeFromDatabento422_NoAdjust(t *testing.T) {
	orig := dbn_hist.SubmitJobParams{
		DateRange: dbn_hist.DateRange{
			Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
		},
	}
	err := &httpStatusError{
		StatusCode: http.StatusUnprocessableEntity,
		Status:     "422 Unprocessable Entity",
		Body:       `{"detail":{"case":"some_other_error","payload":{"available_end":"2026-03-07T00:00:00.000000000Z"}}}`,
	}

	gotParams, ok := tryAdjustRangeFromDatabento422(&orig, err, 7)
	if ok {
		t.Fatal("did not expect adjustment")
	}
	if !gotParams.DateRange.End.Equal(orig.DateRange.End) || !gotParams.DateRange.Start.Equal(orig.DateRange.Start) {
		t.Fatal("range should remain unchanged when no adjustment is applied")
	}
}

func TestSymbolValidation(t *testing.T) {
	tests := []struct {
		name    string
		symbol  string
		isValid bool
	}{
		{"valid simple", testSymbolAAPL, true},
		{"valid with dot", "BRK.A", true},
		{"valid with hyphen", "BF-B", true},
		{"valid single char", "T", true},
		{"valid with number", "X1", true},
		{"empty", "", false},
		{"too long", "ABCDEFGHIJK", false},
		{"has space", "AA PL", false},
		{"has ampersand", "AAPL&X=1", false},
		{"has slash", "AAPL/X", false},
		{"lowercase", "aapl", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := symbolRegex.MatchString(tt.symbol)
			if got != tt.isValid {
				t.Errorf("symbolRegex.MatchString(%q) = %v, want %v", tt.symbol, got, tt.isValid)
			}
		})
	}
}

func TestHTTPStatusError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  httpStatusError
		want string
	}{
		{
			name: "typical 404",
			err:  httpStatusError{StatusCode: 404, Status: "404 Not Found", Body: "page not found"},
			want: "HTTP 404 404 Not Found page not found",
		},
		{
			name: "500 with body",
			err:  httpStatusError{StatusCode: 500, Status: "500 Internal Server Error", Body: `{"error":"boom"}`},
			want: `HTTP 500 500 Internal Server Error {"error":"boom"}`,
		},
		{
			name: "empty body",
			err:  httpStatusError{StatusCode: 502, Status: "502 Bad Gateway", Body: ""},
			want: "HTTP 502 502 Bad Gateway ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizePort(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", "5000"},
		{"valid 8080", "8080", "8080"},
		{"zero", "0", "5000"},
		{"above max", "65536", "5000"},
		{"negative", "-1", "5000"},
		{"non-numeric", "abc", "5000"},
		{"min valid", "1", "1"},
		{"max valid", "65535", "65535"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizePort(tt.input)
			if got != tt.want {
				t.Errorf("normalizePort(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEscapeLinkURLMarkdownV2(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain URL unchanged", "https://example.com/path", "https://example.com/path"},
		{"URL with close paren", "https://example.com/foo)", `https://example.com/foo\)`},
		{"URL with double backslash", `https://example.com/a\\b`, `https://example.com/a\\\\b`},
		{"both double backslash and paren", `https://example.com/a\\b_(c)`, `https://example.com/a\\\\b_(c\)`},
		{"URL with bracket", "https://example.com/report[1]", `https://example.com/report\[1\]`},
		{"URL with both brackets", "https://exa.ai/search?q=[AAPL]", `https://exa.ai/search?q=\[AAPL\]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeLinkURLMarkdownV2(tt.input)
			if got != tt.want {
				t.Errorf("escapeLinkURLMarkdownV2(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLogIncomingUpdate(t *testing.T) {
	t.Run("nil update", func(t *testing.T) {
		logIncomingUpdate(nil, false)
	})

	t.Run("message update", func(t *testing.T) {
		logIncomingUpdate(&models.Update{
			ID: 1,
			Message: &models.Message{
				Text: "hello",
				Chat: models.Chat{Type: models.ChatTypePrivate},
			},
		}, true)
	})

	t.Run("callback update", func(t *testing.T) {
		logIncomingUpdate(&models.Update{
			ID:            2,
			CallbackQuery: &models.CallbackQuery{},
		}, false)
	})

	t.Run("empty update", func(t *testing.T) {
		logIncomingUpdate(&models.Update{ID: 3}, false)
	})
}

func TestLogUnmatchedMessage(t *testing.T) {
	prevMention := botMention
	defer func() { botMention = prevMention }()
	botMention = "@testbot"

	t.Run("nil update", func(t *testing.T) {
		logUnmatchedMessage(nil)
	})

	t.Run("nil message", func(t *testing.T) {
		logUnmatchedMessage(&models.Update{})
	})

	t.Run("message with entities", func(t *testing.T) {
		logUnmatchedMessage(&models.Update{
			Message: &models.Message{
				Text: "@testbot hello",
				Chat: models.Chat{ID: -1001, Type: models.ChatTypeGroup},
				Entities: []models.MessageEntity{
					{Type: models.MessageEntityTypeMention, Offset: 0, Length: 8},
				},
			},
		})
	})
}

func TestLogAllowedGroups(t *testing.T) {
	prev := allowedGroups
	defer func() { allowedGroups = prev }()

	allowedGroups = map[int64]struct{}{
		-1001: {},
		-1002: {},
	}

	logAllowedGroups("test heartbeat")

	// Verify no panic — if we got here the function worked.
	_ = fmt.Sprintf("logged %d groups", len(allowedGroups))
}

func TestFetchFinancialMetrics_Success(t *testing.T) {
	mockMetrics := financialMetricsResponse{
		Metric: FinancialMetrics{
			PEExclExtraTTM:     28.5,
			EPSExclExtraTTM:    6.42,
			NetProfitMarginTTM: 25.8,
			ROETTM:             145.0,
			DebtToEquityTTM:    1.2,
			Beta:               1.3,
			High52W:            260.0,
			Low52W:             164.0,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubMetricsPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("symbol") != testSymbolAAPL {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("metric") != "all" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockMetrics)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchFinancialMetrics(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil FinancialMetrics")
	}
	if result.PEExclExtraTTM != 28.5 {
		t.Errorf("expected PE 28.5, got %f", result.PEExclExtraTTM)
	}
	if result.ROETTM != 145.0 {
		t.Errorf("expected ROE 145.0, got %f", result.ROETTM)
	}
}

func TestFetchFinancialMetrics_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubMetricsPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	_, err := fetchFinancialMetrics(context.Background(), testSymbolAAPL)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestFetchEarningsHistory_Success(t *testing.T) {
	mockEarnings := []EarningsEntry{
		{Period: testEarningsPeriod1, Actual: 2.40, Estimate: 2.35, Surprise: 0.05, SurprisePct: 2.13},
		{Period: "2025-12-31", Actual: 2.20, Estimate: 2.18, Surprise: 0.02, SurprisePct: 0.92},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubEarningsPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("symbol") != testSymbolAAPL {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockEarnings)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchEarningsHistory(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 earnings entries, got %d", len(result))
	}
	if result[0].Period != "2026-03-31" {
		t.Errorf("expected period 2026-03-31, got %s", result[0].Period)
	}
	if result[0].Actual != 2.40 {
		t.Errorf("expected actual 2.40, got %f", result[0].Actual)
	}
}

func TestFetchEarningsHistory_EmptyArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubEarningsPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]EarningsEntry{})
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchEarningsHistory(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error for empty array: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 entries for empty array, got %d", len(result))
	}
}

func TestFetchRecommendation_Success(t *testing.T) {
	mockRec := []RecommendationTrend{
		{Period: testRecommendPeriod, StrongBuy: 15, Buy: 20, Hold: 5, Sell: 2, StrongSell: 1},
		{Period: "2026-04", StrongBuy: 14, Buy: 19, Hold: 6, Sell: 3, StrongSell: 1},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubRecommendPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("symbol") != testSymbolAAPL {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockRec)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchRecommendation(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil recommendation (first element)")
	}
	if result.Period != testRecommendPeriod {
		t.Errorf("expected first period 2026-05, got %s", result.Period)
	}
	if result.StrongBuy != 15 {
		t.Errorf("expected strongBuy 15, got %d", result.StrongBuy)
	}
}

func TestFetchRecommendation_EmptyArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubRecommendPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]RecommendationTrend{})
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchRecommendation(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatal("expected nil for empty array")
	}
}

func TestFetchRecommendation_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubRecommendPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	_, err := fetchRecommendation(context.Background(), testSymbolAAPL)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestFetchPriceTarget_Success(t *testing.T) {
	mockPT := PriceTarget{
		TargetHigh:   250,
		TargetLow:    200,
		TargetMean:   225,
		TargetMedian: 228,
		CurrentPrice: 187,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubPriceTargetPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("symbol") != testSymbolAAPL {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockPT)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchPriceTarget(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil PriceTarget")
	}
	if result.TargetHigh != 250 {
		t.Errorf("expected targetHigh 250, got %f", result.TargetHigh)
	}
	if result.CurrentPrice != 187 {
		t.Errorf("expected currentPrice 187, got %f", result.CurrentPrice)
	}
}

func TestFetchFinancialMetrics_MissingAPIKey(t *testing.T) {
	t.Setenv("FINNHUB_API_KEY", "")
	_, err := fetchFinancialMetrics(context.Background(), testSymbolAAPL)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestFetchEarningsHistory_MissingAPIKey(t *testing.T) {
	t.Setenv("FINNHUB_API_KEY", "")
	_, err := fetchEarningsHistory(context.Background(), testSymbolAAPL)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestFetchRecommendation_MissingAPIKey(t *testing.T) {
	t.Setenv("FINNHUB_API_KEY", "")
	_, err := fetchRecommendation(context.Background(), testSymbolAAPL)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestFetchPriceTarget_MissingAPIKey(t *testing.T) {
	t.Setenv("FINNHUB_API_KEY", "")
	_, err := fetchPriceTarget(context.Background(), testSymbolAAPL)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestFetchPriceTarget_ZeroTargets_ReturnsNil(t *testing.T) {
	mockPT := PriceTarget{} // All fields zero.

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != finnhubPriceTargetPath {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockPT)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchPriceTarget(context.Background(), testSymbolAAPL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatal("expected nil result for all-zero price target (no analyst coverage)")
	}
}
