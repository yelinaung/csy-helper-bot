package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
)

var (
	httpClient    = &http.Client{Timeout: 10 * time.Second}
	symbolRegex   = regexp.MustCompile(`^[A-Z0-9.\-]{1,10}$`)
	textExplainer *geminiExplainer
)

const (
	finnhubBaseURL     = "https://finnhub.io/api/v1"
	leetCodeGraphQLURL = "https://leetcode.com/graphql"
)

func Run() error {
	_ = godotenv.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, update *models.Update) {
			// Silent handler - do nothing for unmatched updates
			// This suppresses the default "[TGBOT] [UPDATE]" verbose logging
		}),
	}

	b, err := bot.New(token, opts...)
	if err != nil {
		return err
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, helpHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/lc", bot.MatchTypeExact, lcHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!lc", bot.MatchTypeExact, lcHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!s", bot.MatchTypeExact, stockHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!s ", bot.MatchTypePrefix, stockHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "explain me this", bot.MatchTypeExact, explainHandler)

	var initErr error
	textExplainer, initErr = initGeminiExplainer()
	if initErr != nil {
		log.Printf("Gemini explainer disabled: %v", initErr)
	} else {
		log.Println("Gemini explainer initialized")
	}

	go startHealthServer()

	log.Println("Bot started...")
	b.Start(ctx)
	return nil
}

func startHealthServer() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "5000"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	log.Printf("Health server listening on port %s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("Health server error: %v", err)
	}
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
!s SYMBOL - Get stock price (e.g., !s AAPL)
Reply with "explain me this" - Explain the replied message`

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            helpText,
	})
}

func lcHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	question, err := fetchDailyLeetCode(ctx)
	if err != nil {
		log.Printf("Failed to fetch LeetCode daily question: %v", err)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Failed to fetch LeetCode daily question. Please try again later.",
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

	if !symbolRegex.MatchString(symbol) {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Invalid stock symbol. Use 1-10 alphanumeric characters (e.g., AAPL, BRK.A).",
		})
		return
	}

	quote, err := fetchStockQuote(ctx, symbol)
	if err != nil {
		log.Printf("Failed to fetch stock quote for %s: %v", symbol, err)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            fmt.Sprintf("Failed to fetch stock quote for %s. Please try again later.", symbol),
		})
		return
	}

	profile, err := fetchCompanyProfile(ctx, symbol)
	if err != nil {
		log.Printf("Failed to fetch company profile for %s: %v", symbol, err)
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            formatStockMessage(symbol, quote, profile),
	})
}

func initGeminiExplainer() (*geminiExplainer, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("GEMINI_API_KEY not configured")
	}

	return newGeminiExplainer(context.Background(), apiKey)
}

func explainHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if textExplainer == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Explain feature is not configured. Please set GEMINI_API_KEY.",
		})
		return
	}

	quotedText := extractQuotedText(update.Message)
	if quotedText == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            `Reply to a text message and send "explain me this".`,
		})
		return
	}

	explanation, err := textExplainer.explain(ctx, quotedText)
	if err != nil {
		log.Printf("Failed to explain quoted message: %v", err)

		errText := "Failed to explain this message. Please try again later."
		if errors.Is(err, ErrExplainTimeout) {
			errText = "Explanation timed out. Please try again."
		}

		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            errText,
		})
		return
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            explanation,
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
}

func extractQuotedText(message *models.Message) string {
	if message == nil {
		return ""
	}

	if message.ReplyToMessage != nil {
		if txt := strings.TrimSpace(message.ReplyToMessage.Text); txt != "" {
			return txt
		}
		if caption := strings.TrimSpace(message.ReplyToMessage.Caption); caption != "" {
			return caption
		}
	}

	if message.Quote != nil {
		if quoteText := strings.TrimSpace(message.Quote.Text); quoteText != "" {
			return quoteText
		}
	}

	return ""
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

type CompanyProfile struct {
	Name                 string  `json:"name"`
	MarketCapitalization float64 `json:"marketCapitalization"`
	Industry             string  `json:"finnhubIndustry"`
	Exchange             string  `json:"exchange"`
}

func fetchStockQuote(ctx context.Context, symbol string) (*StockQuote, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("FINNHUB_API_KEY not configured")
	}
	return fetchStockQuoteFromURL(ctx, finnhubBaseURL, symbol, apiKey)
}

func fetchStockQuoteFromURL(ctx context.Context, baseURL, symbol, apiKey string) (*StockQuote, error) {
	u, err := url.Parse(baseURL + "/quote")
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

func fetchCompanyProfile(ctx context.Context, symbol string) (*CompanyProfile, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("FINNHUB_API_KEY not configured")
	}
	return fetchCompanyProfileFromURL(ctx, finnhubBaseURL, symbol, apiKey)
}

func fetchCompanyProfileFromURL(ctx context.Context, baseURL, symbol, apiKey string) (*CompanyProfile, error) {
	u, err := url.Parse(baseURL + "/stock/profile2")
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
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var profile CompanyProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, err
	}

	return &profile, nil
}

func formatStockMessage(symbol string, quote *StockQuote, profile *CompanyProfile) string {
	changeEmoji := "🔴"
	if quote.Change >= 0 {
		changeEmoji = "🟢"
	}

	name := symbol
	var marketCapStr string
	var industryStr string

	if profile != nil && profile.Name != "" {
		name = profile.Name
		if profile.MarketCapitalization > 0 {
			marketCapStr = fmt.Sprintf("\n🏢 Market Cap: $%.2fB", profile.MarketCapitalization/1000)
		}
		if profile.Industry != "" {
			industryStr = fmt.Sprintf("\n🏭 Industry: %s", profile.Industry)
		}
	}

	return fmt.Sprintf(`%s (%s) %s
💵 Current: $%.2f
📈 Change: %.2f (%.2f%%)
📊 Open: $%.2f | High: $%.2f | Low: $%.2f
📉 Previous Close: $%.2f%s%s`,
		name, symbol, changeEmoji,
		quote.CurrentPrice,
		quote.Change, quote.PercentChange,
		quote.Open, quote.High, quote.Low,
		quote.PreviousClose,
		marketCapStr, industryStr)
}

func formatLeetCodeMessage(question *LeetCodeQuestion) string {
	difficultyEmoji := map[string]string{
		"Easy":   "🟩",
		"Medium": "🟨",
		"Hard":   "🟥",
	}

	emoji := difficultyEmoji[question.Difficulty]
	date := time.Now().UTC().Format("2006-01-02")
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
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
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

func fetchDailyLeetCode(ctx context.Context) (*LeetCodeQuestion, error) {
	return fetchDailyLeetCodeFromURL(ctx, leetCodeGraphQLURL)
}

func fetchDailyLeetCodeFromURL(ctx context.Context, apiURL string) (*LeetCodeQuestion, error) {
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

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
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

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	q := gqlResp.Data.ActiveDailyCodingChallengeQuestion.Question
	if q.Title == "" || q.TitleSlug == "" || q.Difficulty == "" {
		return nil, fmt.Errorf("daily question data missing")
	}

	return &LeetCodeQuestion{
		Title:      q.Title,
		TitleSlug:  q.TitleSlug,
		Difficulty: q.Difficulty,
	}, nil
}
