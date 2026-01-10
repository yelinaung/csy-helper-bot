package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	question, err := fetchDailyLeetCodeFromURL(server.URL)
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

	_, err := fetchDailyLeetCodeFromURL(server.URL)
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestFetchDailyLeetCode_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	_, err := fetchDailyLeetCodeFromURL(server.URL)
	if err == nil {
		t.Error("expected error for invalid JSON response")
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
			if !contains(msg, tt.question.Title) {
				t.Errorf("message should contain title '%s'", tt.question.Title)
			}
			if !contains(msg, tt.wantEmoji) {
				t.Errorf("message should contain emoji '%s'", tt.wantEmoji)
			}
			if !contains(msg, tt.question.TitleSlug) {
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
	if !contains(msg, expectedURL) {
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

	if !contains(msg, "Date:") {
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
	if !contains(msg, "Unknown") {
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

	msg := formatStockMessage("AAPL", quote)

	if !contains(msg, "AAPL") {
		t.Error("message should contain symbol")
	}
	if !contains(msg, "🟢") {
		t.Error("message should contain green emoji for positive change")
	}
	if !contains(msg, "150.25") {
		t.Error("message should contain current price")
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

	msg := formatStockMessage("MSFT", quote)

	if !contains(msg, "🔴") {
		t.Error("message should contain red emoji for negative change")
	}
	if !contains(msg, "-3.50") {
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

	msg := formatStockMessage("TEST", quote)

	if !contains(msg, "Current:") {
		t.Error("message should contain Current label")
	}
	if !contains(msg, "Change:") {
		t.Error("message should contain Change label")
	}
	if !contains(msg, "Open:") {
		t.Error("message should contain Open label")
	}
	if !contains(msg, "High:") {
		t.Error("message should contain High label")
	}
	if !contains(msg, "Low:") {
		t.Error("message should contain Low label")
	}
	if !contains(msg, "Previous Close:") {
		t.Error("message should contain Previous Close label")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && searchString(s, substr)))
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
