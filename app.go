package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	opts := []bot.Option{}

	b, err := bot.New(token, opts...)
	if err != nil {
		log.Fatal(err)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, helpHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/lc", bot.MatchTypeExact, lcHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!lc", bot.MatchTypeExact, lcHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!s ", bot.MatchTypePrefix, stockHandler)

	log.Println("Bot started...")
	b.Start(ctx)
}

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            "Welcome! I'm your helper bot. Use /help to see what I can do.",
	})
}

func helpHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	helpText := `Available commands:
/start - Start the bot
/help - Show this help message
/lc - Get today's LeetCode daily challenge
!s SYMBOL - Get stock price (e.g., !s AAPL)`

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            helpText,
	})
}

func lcHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	question, err := fetchDailyLeetCode()
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            fmt.Sprintf("Failed to fetch LeetCode daily question: %v", err),
		})
		return
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            formatLeetCodeMessage(question),
	})
}

func stockHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	symbol := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "!s "))
	symbol = strings.ToUpper(symbol)

	if symbol == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Please provide a stock symbol. Usage: !s AAPL",
		})
		return
	}

	quote, err := fetchStockQuote(symbol)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            fmt.Sprintf("Failed to fetch stock quote: %v", err),
		})
		return
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            formatStockMessage(symbol, quote),
	})
}

type StockQuote struct {
	CurrentPrice  float64 `json:"c"`
	Change        float64 `json:"d"`
	PercentChange float64 `json:"dp"`
	High          float64 `json:"h"`
	Low           float64 `json:"l"`
	Open          float64 `json:"o"`
	PreviousClose float64 `json:"pc"`
}

func fetchStockQuote(symbol string) (*StockQuote, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("FINNHUB_API_KEY not configured")
	}

	url := fmt.Sprintf("https://finnhub.io/api/v1/quote?symbol=%s&token=%s", symbol, apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var quote StockQuote
	if err := json.NewDecoder(resp.Body).Decode(&quote); err != nil {
		return nil, err
	}

	if quote.CurrentPrice == 0 {
		return nil, fmt.Errorf("symbol not found or no data available")
	}

	return &quote, nil
}

func formatStockMessage(symbol string, quote *StockQuote) string {
	changeEmoji := "🔴"
	if quote.Change >= 0 {
		changeEmoji = "🟢"
	}

	return fmt.Sprintf(`%s %s
💵 Current: $%.2f
📈 Change: %.2f (%.2f%%)
📊 Open: $%.2f | High: $%.2f | Low: $%.2f
📉 Previous Close: $%.2f`,
		symbol, changeEmoji,
		quote.CurrentPrice,
		quote.Change, quote.PercentChange,
		quote.Open, quote.High, quote.Low,
		quote.PreviousClose)
}

func formatLeetCodeMessage(question *LeetCodeQuestion) string {
	difficultyEmoji := map[string]string{
		"Easy":   "🟩",
		"Medium": "🟨",
		"Hard":   "🟥",
	}

	emoji := difficultyEmoji[question.Difficulty]
	date := time.Now().Format("2006-01-02")
	url := fmt.Sprintf("https://leetcode.com/problems/%s/", question.TitleSlug)

	return fmt.Sprintf("Date: %s\nTitle: %s\nDifficulty: %s %s\n%s",
		date, question.Title, question.Difficulty, emoji, url)
}

type LeetCodeQuestion struct {
	Title      string
	TitleSlug  string
	Difficulty string
}

type graphQLRequest struct {
	Query string `json:"query"`
}

type graphQLResponse struct {
	Data struct {
		ActiveDailyCodingChallengeQuestion struct {
			Question struct {
				Title      string `json:"title"`
				TitleSlug  string `json:"titleSlug"`
				Difficulty string `json:"difficulty"`
			} `json:"question"`
		} `json:"activeDailyCodingChallengeQuestion"`
	} `json:"data"`
}

const leetCodeGraphQLURL = "https://leetcode.com/graphql"

func fetchDailyLeetCode() (*LeetCodeQuestion, error) {
	return fetchDailyLeetCodeFromURL(leetCodeGraphQLURL)
}

func fetchDailyLeetCodeFromURL(url string) (*LeetCodeQuestion, error) {
	query := `{
		activeDailyCodingChallengeQuestion {
			question {
				title
				titleSlug
				difficulty
			}
		}
	}`

	reqBody := graphQLRequest{Query: query}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, err
	}

	q := gqlResp.Data.ActiveDailyCodingChallengeQuestion.Question
	return &LeetCodeQuestion{
		Title:      q.Title,
		TitleSlug:  q.TitleSlug,
		Difficulty: q.Difficulty,
	}, nil
}
