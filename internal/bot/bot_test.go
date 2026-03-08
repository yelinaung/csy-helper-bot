package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
		Name:                 "Apple Inc",
		MarketCapitalization: 3000000,
		Industry:             "Technology",
	}

	msg := formatStockMessage("AAPL", quote, profile)

	if !strings.Contains(msg, "AAPL") {
		t.Error("message should contain symbol")
	}
	if !strings.Contains(msg, "Apple Inc") {
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
		if r.URL.Query().Get("symbol") != "AAPL" {
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

	result, err := fetchStockQuote(context.Background(), "AAPL")
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

	_, err := fetchStockQuote(context.Background(), "AAPL")
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
		Name:                 "Apple Inc",
		MarketCapitalization: 3000000,
		Industry:             "Technology",
		Exchange:             "NASDAQ",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "AAPL" {
			t.Errorf("expected symbol=AAPL, got %s", r.URL.Query().Get("symbol"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockProfile)
	}))
	defer server.Close()

	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	result, err := fetchCompanyProfile(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Name != "Apple Inc" {
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
		{"unblocked symbol", "AAPL", false, ""},
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
		{name: "spot quote", input: "!s AAPL", wantSym: "AAPL", wantDays: 0},
		{name: "historical 7d", input: "!s AAPL 7d", wantSym: "AAPL", wantDays: 7},
		{name: "historical 30d", input: "!s msft 30d", wantSym: "MSFT", wantDays: 30},
		{name: "tab after command", input: "!s\tAAPL", wantSym: "AAPL", wantDays: 0},
		{name: "newline after command with range", input: "!s\nAAPL 7d", wantSym: "AAPL", wantDays: 7},
		{name: "missing separator after command", input: "!sAAPL", wantError: true, errSubstr: "invalid usage"},
		{name: "invalid range", input: "!s AAPL 10d", wantError: true, errSubstr: "invalid range"},
		{name: "invalid range 1d", input: "!s AAPL 1d", wantError: true, errSubstr: "invalid range"},
		{name: "invalid range 365d", input: "!s AAPL 365d", wantError: true, errSubstr: "invalid range"},
		{name: "invalid symbol chars", input: "!s $$$", wantError: true, errSubstr: "invalid stock symbol"},
		{name: "invalid symbol punctuation", input: "!s AAPL!", wantError: true, errSubstr: "invalid stock symbol"},
		{name: "invalid symbol with extra token", input: "!s AA PL", wantError: true, errSubstr: "invalid usage"},
		{name: "second symbol token", input: "!s AAPL MSFT", wantError: true, errSubstr: "invalid usage"},
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
	buf, err := renderHistoricalChartPNG("AAPL", 7, bars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(buf) == 0 {
		t.Fatal("expected non-empty PNG bytes")
	}
}

func TestFormatHistoricalSummary(t *testing.T) {
	bars := []HistoricalBar{
		{Date: time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), Close: 100, High: 102, Low: 99},
		{Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Close: 110, High: 111, Low: 98},
	}
	got := formatHistoricalSummary("AAPL", 7, bars)
	if !strings.Contains(got, "AAPL 7d") {
		t.Fatalf("expected symbol and range in summary, got %q", got)
	}
	if !strings.Contains(got, "Return: 10.00%") {
		t.Fatalf("expected computed return in summary, got %q", got)
	}
}

func TestFormatHistoricalSummary_EmptyBars(t *testing.T) {
	got := formatHistoricalSummary("AAPL", 7, nil)
	if !strings.Contains(got, "No historical data returned") {
		t.Fatalf("expected empty-data message, got %q", got)
	}
}

func TestHistoricalDateRangeUTC(t *testing.T) {
	now := time.Date(2026, 3, 8, 15, 4, 5, 0, time.FixedZone("UTC+8", 8*3600))
	got := historicalDateRangeUTC(now, 7)
	wantEnd := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	wantStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	if !got.End.Equal(wantEnd) {
		t.Fatalf("end mismatch: got %s want %s", got.End, wantEnd)
	}
	if !got.Start.Equal(wantStart) {
		t.Fatalf("start mismatch: got %s want %s", got.Start, wantStart)
	}
}

func TestSymbolValidation(t *testing.T) {
	tests := []struct {
		name    string
		symbol  string
		isValid bool
	}{
		{"valid simple", "AAPL", true},
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
