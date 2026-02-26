package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

var (
	httpClient    = &http.Client{Timeout: 10 * time.Second}
	symbolRegex   = regexp.MustCompile(`^[A-Z0-9.\-]{1,10}$`)
	textExplainer *geminiExplainer
	botMention    string
	allowedGroups map[int64]struct{}
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
			logIncomingUpdate(update, false)
			enforceChatAccess(ctx, b, update)
		}),
	}

	b, err := bot.New(token, opts...)
	if err != nil {
		return err
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, startHandler, requestLoggingMiddleware)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, helpHandler, requestLoggingMiddleware)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/lc", bot.MatchTypeExact, lcHandler, requestLoggingMiddleware)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!lc", bot.MatchTypeExact, lcHandler, requestLoggingMiddleware)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!s", bot.MatchTypeExact, stockHandler, requestLoggingMiddleware)
	b.RegisterHandler(bot.HandlerTypeMessageText, "!s ", bot.MatchTypePrefix, stockHandler, requestLoggingMiddleware)

	me, err := b.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch bot profile: %w", err)
	}
	if me.Username != "" {
		botMention = "@" + strings.ToLower(me.Username)
	}
	b.RegisterHandlerMatchFunc(shouldHandleExplainMention, explainHandler, requestLoggingMiddleware)

	allowedGroups, err = parseAllowedGroupIDs(os.Getenv("ALLOWED_GROUP_IDS"))
	if err != nil {
		return fmt.Errorf("failed to parse ALLOWED_GROUP_IDS: %w", err)
	}
	logAllowedGroups("Loaded allowed group configuration")

	var initErr error
	textExplainer, initErr = initGeminiExplainer()
	if initErr != nil {
		log.Warn().Err(initErr).Msg("Gemini explainer disabled")
	} else {
		log.Info().Msg("Gemini explainer initialized")
	}

	go startHealthServer()
	go startAllowedGroupsReporter(ctx)

	log.Info().Msg("Bot started")
	b.Start(ctx)
	return nil
}

func startHealthServer() {
	port := normalizePort(os.Getenv("PORT"))

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Msg("incoming HTTP request")
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

	log.Info().Msg("Health server listening")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error().Err(err).Msg("Health server error")
	}
}

func normalizePort(raw string) string {
	const defaultPort = "5000"
	if raw == "" {
		return defaultPort
	}

	p, err := strconv.Atoi(raw)
	if err != nil || p < 1 || p > 65535 {
		return defaultPort
	}

	return strconv.Itoa(p)
}

func requestLoggingMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		logIncomingUpdate(update, true)
		if !enforceChatAccess(ctx, b, update) {
			return
		}
		next(ctx, b, update)
	}
}

func parseAllowedGroupIDs(raw string) (map[int64]struct{}, error) {
	result := make(map[int64]struct{})
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}

	for token := range strings.SplitSeq(raw, ",") {
		idText := strings.TrimSpace(token)
		if idText == "" {
			continue
		}

		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid group id %q: %w", idText, err)
		}
		result[id] = struct{}{}
	}

	return result, nil
}

func enforceChatAccess(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	chat := extractChatFromUpdate(update)
	if chat == nil {
		return true
	}
	if !isGroupLikeChat(chat.Type) {
		log.Info().
			Int64("chat_id", chat.ID).
			Str("chat_type", string(chat.Type)).
			Msg("Ignoring non-group chat")
		return false
	}
	if _, ok := allowedGroups[chat.ID]; ok {
		log.Info().
			Int64("chat_id", chat.ID).
			Str("chat_type", string(chat.Type)).
			Bool("allowed", true).
			Msg("Group activity")
		return true
	}

	log.Warn().
		Int64("chat_id", chat.ID).
		Str("chat_type", string(chat.Type)).
		Bool("allowed", false).
		Msg("Group activity; leaving unauthorized group")
	_, err := b.LeaveChat(ctx, &bot.LeaveChatParams{ChatID: chat.ID})
	if err != nil {
		log.Error().
			Err(err).
			Int64("chat_id", chat.ID).
			Msg("Failed to leave unauthorized group")
	}
	return false
}

func extractChatFromUpdate(update *models.Update) *models.Chat {
	if update == nil {
		return nil
	}
	if update.Message != nil {
		return &update.Message.Chat
	}
	if update.EditedMessage != nil {
		return &update.EditedMessage.Chat
	}
	if update.ChannelPost != nil {
		return &update.ChannelPost.Chat
	}
	if update.EditedChannelPost != nil {
		return &update.EditedChannelPost.Chat
	}
	if update.MyChatMember != nil {
		return &update.MyChatMember.Chat
	}
	if update.ChatMember != nil {
		return &update.ChatMember.Chat
	}
	if update.ChatJoinRequest != nil {
		return &update.ChatJoinRequest.Chat
	}
	if update.CallbackQuery != nil && update.CallbackQuery.Message.Type == models.MaybeInaccessibleMessageTypeMessage &&
		update.CallbackQuery.Message.Message != nil {
		return &update.CallbackQuery.Message.Message.Chat
	}
	return nil
}

func isGroupLikeChat(chatType models.ChatType) bool {
	return chatType == models.ChatTypeGroup || chatType == models.ChatTypeSupergroup
}

func startAllowedGroupsReporter(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logAllowedGroups("Allowed group configuration heartbeat")
		}
	}
}

func logAllowedGroups(message string) {
	ids := make([]int64, 0, len(allowedGroups))
	for id := range allowedGroups {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	log.Info().
		Int("allowed_group_count", len(ids)).
		Interface("allowed_group_ids", ids).
		Msg(message)
}

func logIncomingUpdate(update *models.Update, matched bool) {
	if update == nil {
		log.Info().Bool("matched_handler", matched).Msg("incoming telegram update")
		return
	}

	event := log.Info().
		Int64("update_id", update.ID).
		Bool("matched_handler", matched)

	switch {
	case update.Message != nil:
		event = event.
			Str("update_type", "message").
			Str("chat_type", string(update.Message.Chat.Type)).
			Bool("has_text", update.Message.Text != "").
			Bool("is_reply", update.Message.ReplyToMessage != nil)
	case update.CallbackQuery != nil:
		event = event.Str("update_type", "callback_query")
	default:
		event = event.Str("update_type", "other")
	}

	event.Msg("incoming telegram update")
}

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            "Welcome! I'm your helper bot. Use /help to see what I can do.",
	})
}

func helpHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	helpText := fmt.Sprintf(`Available commands:
/start - Start the bot
/help - Show this help message
/lc - Get today's LeetCode daily challenge
!s SYMBOL - Get stock price (e.g., !s AAPL)
Mention + "explain me this" - Explain the replied message (e.g., @%s explain me this)`, strings.TrimPrefix(botMention, "@"))

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            helpText,
	})
}

func lcHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	question, err := fetchDailyLeetCode(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to fetch LeetCode daily question")
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
		log.Error().Err(err).Str("symbol", symbol).Msg("Failed to fetch stock quote")
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            fmt.Sprintf("Failed to fetch stock quote for %s. Please try again later.", symbol),
		})
		return
	}

	profile, err := fetchCompanyProfile(ctx, symbol)
	if err != nil {
		log.Warn().Err(err).Str("symbol", symbol).Msg("Failed to fetch company profile")
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
			Text:            fmt.Sprintf(`Reply to a text message and send "%s explain me this".`, botMention),
		})
		return
	}

	thinkingMsg, thinkingErr := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            "thinking...",
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})

	respondInBurmese := shouldRespondInBurmese(update.Message.Text, quotedText)
	explanation, err := textExplainer.explainWithLanguage(ctx, quotedText, respondInBurmese)
	if err != nil {
		log.Error().Err(err).Msg("Failed to explain quoted message")

		errText := "Failed to explain this message. Please try again later."
		if errors.Is(err, ErrExplainTimeout) {
			errText = "Explanation timed out. Please try again."
		}

		sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, errText)
		return
	}

	sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, explanation)
}

func sendOrEditExplainResult(
	ctx context.Context,
	b *bot.Bot,
	update *models.Update,
	thinkingMsg *models.Message,
	thinkingErr error,
	text string,
) {
	if thinkingErr == nil && thinkingMsg != nil {
		_, editErr := b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    update.Message.Chat.ID,
			MessageID: thinkingMsg.ID,
			Text:      text,
		})
		if editErr == nil {
			return
		}
		log.Warn().Err(editErr).Msg("Failed to edit thinking message; falling back to send message")
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            text,
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

func shouldRespondInBurmese(texts ...string) bool {
	for _, text := range texts {
		for _, r := range text {
			// Myanmar script blocks.
			if (r >= 0x1000 && r <= 0x109F) || (r >= 0xAA60 && r <= 0xAA7F) || (r >= 0xA9E0 && r <= 0xA9FF) {
				return true
			}
		}
	}
	return false
}

func shouldHandleExplainMention(update *models.Update) bool {
	if update == nil || update.Message == nil {
		return false
	}
	if botMention == "" {
		return false
	}

	text := strings.ToLower(strings.TrimSpace(update.Message.Text))
	if text == "" || !strings.Contains(text, "explain me this") {
		return false
	}

	for _, entity := range update.Message.Entities {
		if entity.Type != models.MessageEntityTypeMention {
			continue
		}
		if entity.Offset < 0 || entity.Length <= 0 || entity.Offset+entity.Length > len(update.Message.Text) {
			continue
		}

		mention := strings.ToLower(update.Message.Text[entity.Offset : entity.Offset+entity.Length])
		if mention == botMention {
			return true
		}
	}

	return false
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
	MarketCapitalization float64 `json:"marketCapitalization"` //nolint:tagliatelle // Finnhub response uses camelCase.
	Industry             string  `json:"finnhubIndustry"`      //nolint:tagliatelle // Finnhub response uses camelCase.
	Exchange             string  `json:"exchange"`
}

func fetchStockQuote(ctx context.Context, symbol string) (*StockQuote, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
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

	resp, err := httpClient.Do(req) //nolint:gosec // URL host is controlled by trusted call sites; overridable helper is for tests.
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
		return nil, errors.New("symbol not found or no data available")
	}

	return &quote, nil
}

func fetchCompanyProfile(ctx context.Context, symbol string) (*CompanyProfile, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
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

	resp, err := httpClient.Do(req) //nolint:gosec // URL host is controlled by trusted call sites; overridable helper is for tests.
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
			industryStr = "\n🏭 Industry: " + profile.Industry
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
				TitleSlug  string `json:"titleSlug"` //nolint:tagliatelle // LeetCode GraphQL response uses camelCase.
				Difficulty string `json:"difficulty"`
			} `json:"question"`
		} `json:"activeDailyCodingChallengeQuestion"` //nolint:tagliatelle // LeetCode GraphQL response uses camelCase.
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

	resp, err := httpClient.Do(req) //nolint:gosec // URL host is controlled by trusted call sites; overridable helper is for tests.
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
		return nil, errors.New("daily question data missing")
	}

	return &LeetCodeQuestion{
		Title:      q.Title,
		TitleSlug:  q.TitleSlug,
		Difficulty: q.Difficulty,
	}, nil
}
