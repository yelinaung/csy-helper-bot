package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	question, err := fetchDailyLeetCodeFromURL(context.Background(), server.URL)
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

	_, err := fetchDailyLeetCodeFromURL(context.Background(), server.URL)
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestFetchDailyLeetCode_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	_, err := fetchDailyLeetCodeFromURL(context.Background(), server.URL)
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

	_, err := fetchDailyLeetCodeFromURL(context.Background(), server.URL)
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

	_, err := fetchDailyLeetCodeFromURL(context.Background(), server.URL)
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

	result, err := fetchStockQuoteFromURL(context.Background(), server.URL, "AAPL", "test-key")
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

	_, err := fetchStockQuoteFromURL(context.Background(), server.URL, "AAPL", "test-key")
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

	_, err := fetchStockQuoteFromURL(context.Background(), server.URL, "INVALID", "test-key")
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

	result, err := fetchCompanyProfileFromURL(context.Background(), server.URL, "AAPL", "test-key")
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
