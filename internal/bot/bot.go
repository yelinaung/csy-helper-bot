package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	dbn "github.com/NimbleMarkets/dbn-go"
	dbn_hist "github.com/NimbleMarkets/dbn-go/hist"
	"github.com/go-analyze/charts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

var (
	httpClient     = &http.Client{Timeout: 10 * time.Second}
	histHTTPClient = &http.Client{Timeout: 30 * time.Second}
	symbolRegex    = regexp.MustCompile(`^[A-Z0-9.\-]{1,10}$`)
	textExplainer  *geminiExplainer
	explainLimiter *memoryRateLimiter
	botMention     string
	botUserID      int64
	allowedGroups  map[int64]struct{}
	nowFunc        = time.Now
)

var errDatabentoAPIKeyNotConfigured = errors.New("databento api key not configured")

const (
	finnhubBaseURL     = "https://finnhub.io/api/v1"
	leetCodeGraphQLURL = "https://leetcode.com/graphql"
)

var (
	markdownCodeBlockRE  = regexp.MustCompile("(?s)```(.*?)```")
	markdownInlineCodeRE = regexp.MustCompile("`([^`\n]+)`")
	markdownLinkRE       = regexp.MustCompile(`\[(.+?)\]\((https?://[^)\s]+)\)`)
	markdownBoldRE       = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	markdownItalicRE     = regexp.MustCompile(`\*([^*\n]+)\*|_([^_\n]+)_`)
	rangeTokenRE         = regexp.MustCompile(`^[0-9]+d$`)
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
			if !enforceChatAccess(ctx, b, update) {
				return
			}
			logUnmatchedMessage(update)
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
	botUserID = me.ID
	b.RegisterHandlerMatchFunc(shouldHandleAskMention, askHandler, requestLoggingMiddleware)

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
	explainLimiter = loadExplainRateLimiter()

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
	ticker := time.NewTicker(1 * time.Hour)
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
		log.Info().Bool("matched_handler", matched).Msg("Incoming telegram update")
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

	event.Msg("Incoming telegram update")
}

func logUnmatchedMessage(update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}

	msg := update.Message
	hasBotMentionText := botMention != "" && strings.Contains(strings.ToLower(msg.Text), strings.ToLower(botMention))
	hasMentionEntity := false

	for _, entity := range msg.Entities {
		if entity.Type != models.MessageEntityTypeMention {
			continue
		}
		mention, _, ok := mentionAndSuffixAtEntity(msg.Text, &entity)
		if !ok {
			continue
		}
		if strings.EqualFold(mention, botMention) {
			hasMentionEntity = true
			break
		}
	}

	log.Debug().
		Int64("chat_id", msg.Chat.ID).
		Str("chat_type", string(msg.Chat.Type)).
		Int("text_len", len(msg.Text)).
		Int("entity_count", len(msg.Entities)).
		Bool("has_bot_mention_text", hasBotMentionText).
		Bool("has_bot_mention_entity", hasMentionEntity).
		Bool("is_reply", msg.ReplyToMessage != nil).
		Msg("Unmatched incoming message")
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
!s SYMBOL 7d|30d - Get historical chart image (e.g., !s AAPL 7d)
Mention + question - Ask anything (e.g., @%s what is a mutex?)`, strings.TrimPrefix(botMention, "@"))

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
	symbol, days, err := parseStockCommand(update.Message.Text)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            err.Error(),
		})
		return
	}

	if msg, blocked := blockedStockResponse(symbol); blocked {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            msg,
			ReplyParameters: &models.ReplyParameters{
				MessageID:                update.Message.ID,
				AllowSendingWithoutReply: true,
			},
		})
		return
	}

	if days > 0 {
		handleHistoricalStock(ctx, b, update, symbol, days)
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
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
}

func handleHistoricalStock(ctx context.Context, b *bot.Bot, update *models.Update, symbol string, days int) {
	bars, err := fetchHistoricalBars(ctx, symbol, days)
	if err != nil {
		msg := fmt.Sprintf("Failed to fetch %d-day historical data for %s. Please try again later.", days, symbol)
		if errors.Is(err, errDatabentoAPIKeyNotConfigured) {
			msg = "Historical data is unavailable: DATABENTO_API_KEY is not configured."
		}
		log.Error().Err(err).Str("symbol", symbol).Int("days", days).Msg("Failed to fetch historical bars")
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			ReplyParameters: &models.ReplyParameters{
				MessageID:                update.Message.ID,
				AllowSendingWithoutReply: true,
			},
			Text: msg,
		})
		return
	}
	if len(bars) == 0 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            fmt.Sprintf("No historical data returned for %s in the last %d days.", symbol, days),
			ReplyParameters: &models.ReplyParameters{
				MessageID:                update.Message.ID,
				AllowSendingWithoutReply: true,
			},
		})
		return
	}

	caption := formatHistoricalSummary(symbol, days, bars)
	chartPNG, err := renderHistoricalChartPNG(symbol, days, bars)
	if err != nil {
		log.Warn().Err(err).Str("symbol", symbol).Int("days", days).Msg("Failed to render historical chart; sending text only")
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            caption,
			ReplyParameters: &models.ReplyParameters{
				MessageID:                update.Message.ID,
				AllowSendingWithoutReply: true,
			},
		})
		return
	}

	_, sendErr := b.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Photo: &models.InputFileUpload{
			Filename: fmt.Sprintf("%s-%dd.png", strings.ToLower(symbol), days),
			Data:     bytes.NewReader(chartPNG),
		},
		Caption: caption,
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
	if sendErr == nil {
		return
	}

	log.Warn().Err(sendErr).Str("symbol", symbol).Int("days", days).Msg("Failed to send historical chart image; sending text only")
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            caption,
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
}

func parseStockCommand(text string) (string, int, error) {
	if strings.HasPrefix(text, "!s") && len(text) > 2 {
		if text[2] != ' ' {
			return "", 0, errors.New("invalid usage, use !s SYMBOL or !s SYMBOL 7d|30d")
		}
	}

	raw := strings.TrimSpace(strings.TrimPrefix(text, "!s"))
	if raw == "" {
		return "", 0, errors.New("please provide a stock symbol, usage: !s AAPL or !s AAPL 7d")
	}

	parts := strings.Fields(raw)
	if len(parts) > 2 {
		return "", 0, errors.New("invalid usage, use !s SYMBOL or !s SYMBOL 7d|30d")
	}

	symbol := strings.ToUpper(parts[0])
	if !symbolRegex.MatchString(symbol) {
		return "", 0, errors.New("invalid stock symbol, use 1-10 characters: letters, numbers, dots (.) or dashes (-), e.g., AAPL, BRK.A")
	}

	if len(parts) == 1 {
		return symbol, 0, nil
	}

	switch strings.ToLower(parts[1]) {
	case "7d":
		return symbol, 7, nil
	case "30d":
		return symbol, 30, nil
	default:
		if rangeTokenRE.MatchString(strings.ToLower(parts[1])) {
			return "", 0, errors.New("invalid range, use 7d or 30d (e.g., !s AAPL 7d)")
		}
		return "", 0, errors.New("invalid usage, use !s SYMBOL or !s SYMBOL 7d|30d")
	}
}

func initGeminiExplainer() (*geminiExplainer, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("GEMINI_API_KEY not configured")
	}

	return newGeminiExplainer(context.Background(), apiKey)
}

func sendOrEditExplainResult(
	ctx context.Context,
	b *bot.Bot,
	update *models.Update,
	thinkingMsg *models.Message,
	thinkingErr error,
	text string,
) {
	formatted := formatTelegramMarkdown(text)

	if thinkingErr == nil && thinkingMsg != nil {
		_, editErr := b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    update.Message.Chat.ID,
			MessageID: thinkingMsg.ID,
			Text:      formatted,
			ParseMode: models.ParseModeMarkdown,
		})
		if editErr == nil {
			return
		}
		log.Warn().
			Err(editErr).
			Int64("chat_id", update.Message.Chat.ID).
			Int("message_id", thinkingMsg.ID).
			Msg("Failed to edit markdown response; trying escaped fallback")

		_, escapedEditErr := b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    update.Message.Chat.ID,
			MessageID: thinkingMsg.ID,
			Text:      text,
		})
		if escapedEditErr == nil {
			return
		}
		log.Warn().
			Err(escapedEditErr).
			Int64("chat_id", update.Message.Chat.ID).
			Int("message_id", thinkingMsg.ID).
			Msg("Failed to edit plain-text fallback; falling back to send message")
	}

	_, sendErr := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            formatted,
		ParseMode:       models.ParseModeMarkdown,
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
	if sendErr == nil {
		return
	}

	log.Warn().
		Err(sendErr).
		Int64("chat_id", update.Message.Chat.ID).
		Msg("Failed to send markdown response; trying escaped fallback")
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

func formatTelegramMarkdown(text string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	tokens := make([]string, 0, 16)

	addToken := func(value string) string {
		id := len(tokens)
		tokens = append(tokens, value)
		return fmt.Sprintf("TGMARKTOKEN%dX", id)
	}

	normalized = markdownCodeBlockRE.ReplaceAllStringFunc(normalized, func(match string) string {
		submatches := markdownCodeBlockRE.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}
		return addToken("```" + escapeCodeMarkdownV2(submatches[1]) + "```")
	})

	normalized = markdownInlineCodeRE.ReplaceAllStringFunc(normalized, func(match string) string {
		submatches := markdownInlineCodeRE.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}
		return addToken("`" + escapeCodeMarkdownV2(submatches[1]) + "`")
	})

	normalized = markdownLinkRE.ReplaceAllStringFunc(normalized, func(match string) string {
		submatches := markdownLinkRE.FindStringSubmatch(match)
		if len(submatches) < 3 {
			return match
		}
		label := bot.EscapeMarkdownUnescaped(submatches[1])
		url := escapeLinkURLMarkdownV2(submatches[2])
		return addToken("[" + label + "](" + url + ")")
	})

	normalized = markdownBoldRE.ReplaceAllStringFunc(normalized, func(match string) string {
		submatches := markdownBoldRE.FindStringSubmatch(match)
		if len(submatches) < 3 {
			return match
		}

		inner := strings.TrimSpace(submatches[1])
		if inner == "" {
			inner = strings.TrimSpace(submatches[2])
		}
		if inner == "" {
			return match
		}

		return addToken("*" + bot.EscapeMarkdownUnescaped(inner) + "*")
	})

	normalized = markdownItalicRE.ReplaceAllStringFunc(normalized, func(match string) string {
		submatches := markdownItalicRE.FindStringSubmatch(match)
		if len(submatches) < 3 {
			return match
		}

		inner := strings.TrimSpace(submatches[1])
		if inner == "" {
			inner = strings.TrimSpace(submatches[2])
		}
		if inner == "" {
			return match
		}

		return addToken("_" + bot.EscapeMarkdownUnescaped(inner) + "_")
	})

	escaped := bot.EscapeMarkdownUnescaped(normalized)
	for i, token := range tokens {
		escaped = strings.ReplaceAll(escaped, fmt.Sprintf("TGMARKTOKEN%dX", i), token)
	}

	return escaped
}

func escapeCodeMarkdownV2(text string) string {
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		"`", "\\`",
	)
	return replacer.Replace(text)
}

func escapeLinkURLMarkdownV2(text string) string {
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		`)`, `\)`,
	)
	return replacer.Replace(text)
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

func isQuotedFromBot(message *models.Message) bool {
	if message == nil || message.ReplyToMessage == nil || message.ReplyToMessage.From == nil {
		return false
	}
	if botUserID == 0 {
		return message.ReplyToMessage.From.IsBot
	}
	return message.ReplyToMessage.From.ID == botUserID
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

func shouldHandleAskMention(update *models.Update) bool {
	if update == nil || update.Message == nil {
		return false
	}
	if botMention == "" {
		return false
	}

	if strings.TrimSpace(update.Message.Text) == "" {
		return false
	}
	mention, suffix, ok := extractMentionAndSuffix(update.Message)
	if !ok || !strings.EqualFold(mention, botMention) {
		return false
	}

	after := strings.TrimSpace(suffix)
	if after == "" {
		return false
	}
	afterLower := strings.ToLower(after)
	if afterLower == "ask" || strings.HasPrefix(afterLower, "ask ") {
		return true
	}
	return true
}

func extractAskQuestion(message *models.Message) string {
	if message == nil || botMention == "" {
		return ""
	}

	mention, suffix, ok := extractMentionAndSuffix(message)
	if !ok || !strings.EqualFold(mention, botMention) {
		return ""
	}

	after := strings.TrimSpace(suffix)
	if after == "" {
		return ""
	}
	afterLower := strings.ToLower(after)
	if afterLower == "ask" {
		return ""
	}
	if strings.HasPrefix(afterLower, "ask ") {
		return strings.TrimSpace(after[len("ask "):])
	}

	return after
}

func mentionAndSuffixAtEntity(text string, entity *models.MessageEntity) (mention string, suffix string, ok bool) {
	if entity == nil {
		return "", "", false
	}
	start, end, ok := utf16EntityRangeToByteRange(text, entity.Offset, entity.Length)
	if !ok {
		return "", "", false
	}
	return text[start:end], text[end:], true
}

func extractMentionAndSuffix(message *models.Message) (mention string, suffix string, ok bool) {
	if message == nil || botMention == "" {
		return "", "", false
	}

	text := message.Text
	for _, entity := range message.Entities {
		if entity.Type != models.MessageEntityTypeMention {
			continue
		}
		mention, suffix, ok := mentionAndSuffixAtEntity(text, &entity)
		if ok && strings.EqualFold(mention, botMention) {
			return mention, suffix, true
		}
	}

	return mentionAndSuffixFromText(text, botMention)
}

func mentionAndSuffixFromText(text, targetMention string) (mention string, suffix string, ok bool) {
	if strings.TrimSpace(text) == "" || targetMention == "" {
		return "", "", false
	}

	lowerText := strings.ToLower(text)
	lowerMention := strings.ToLower(targetMention)
	searchFrom := 0

	for searchFrom < len(lowerText) {
		idx := strings.Index(lowerText[searchFrom:], lowerMention)
		if idx == -1 {
			return "", "", false
		}
		start := searchFrom + idx
		end := start + len(lowerMention)
		if hasMentionBoundaries(text, start, end) {
			return text[start:end], text[end:], true
		}
		searchFrom = end
	}

	return "", "", false
}

func hasMentionBoundaries(text string, start, end int) bool {
	if start < 0 || end > len(text) || start >= end {
		return false
	}
	if start > 0 && isTelegramUsernameChar(text[start-1]) {
		return false
	}
	if end < len(text) && isTelegramUsernameChar(text[end]) {
		return false
	}
	return true
}

func isTelegramUsernameChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func utf16EntityRangeToByteRange(text string, offset, length int) (start int, end int, ok bool) {
	if offset < 0 || length <= 0 {
		return 0, 0, false
	}

	targetStart := offset
	targetEnd := offset + length
	curUTF16 := 0
	start = -1
	end = -1

	for i, r := range text {
		if curUTF16 == targetStart && start == -1 {
			start = i
		}
		if curUTF16 == targetEnd {
			end = i
			break
		}

		curUTF16 += utf16UnitsForRune(r)
	}

	if start == -1 && curUTF16 == targetStart {
		start = len(text)
	}
	if end == -1 && curUTF16 == targetEnd {
		end = len(text)
	}

	if start == -1 || end == -1 || start > end || end > len(text) {
		return 0, 0, false
	}

	return start, end, true
}

func utf16UnitsForRune(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

func askHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if textExplainer == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Ask feature is not configured. Please set GEMINI_API_KEY.",
		})
		return
	}

	question := extractAskQuestion(update.Message)
	if question == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            fmt.Sprintf(`Send "%s your question here".`, botMention),
		})
		return
	}

	allowed, retryAfter := allowExplainRequest(update.Message)
	if !allowed {
		var userID int64
		if update.Message.From != nil {
			userID = update.Message.From.ID
		}
		log.Warn().
			Int64("chat_id", update.Message.Chat.ID).
			Int64("user_id", userID).
			Dur("retry_after", retryAfter).
			Msg("Ask request rate limited")
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Rate limit reached for ask requests. Please try again shortly.",
			ReplyParameters: &models.ReplyParameters{
				MessageID:                update.Message.ID,
				AllowSendingWithoutReply: true,
			},
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
	if thinkingErr != nil {
		log.Warn().
			Err(thinkingErr).
			Int64("chat_id", update.Message.Chat.ID).
			Msg("Failed to send thinking message for ask request")
	}

	respondInBurmese := shouldRespondInBurmese(update.Message.Text)
	explanation, err := textExplainer.explainWithLanguage(ctx, "", question, respondInBurmese)
	if err != nil {
		log.Error().Err(err).Msg("Failed to answer ask question")

		errText := "Failed to answer your question. Please try again later."
		if errors.Is(err, ErrExplainTimeout) {
			errText = "Answer timed out. Please try again."
		}

		sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, errText)
		return
	}

	sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, explanation)
}

func allowExplainRequest(message *models.Message) (bool, time.Duration) {
	if message == nil {
		return false, 0
	}
	if explainLimiter == nil {
		return true, 0
	}

	var userID int64
	if message.From != nil {
		userID = message.From.ID
	}

	key := buildExplainRateKey(message.Chat.ID, userID)
	return explainLimiter.allow(key, time.Now())
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

type HistoricalBar struct {
	Date   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume uint64
}

var blockedStocks = map[string]string{}

func blockedStockResponse(symbol string) (string, bool) {
	msg, ok := blockedStocks[symbol]
	return msg, ok
}

func fetchHistoricalBars(ctx context.Context, symbol string, days int) ([]HistoricalBar, error) {
	apiKey := strings.TrimSpace(os.Getenv("DATABENTO_API_KEY"))
	if apiKey == "" {
		return nil, errDatabentoAPIKeyNotConfigured
	}

	dataset := strings.TrimSpace(os.Getenv("DATABENTO_DATASET"))
	if dataset == "" {
		dataset = "EQUS.MINI"
	}

	dateRange := historicalDateRangeUTC(nowFunc(), days)
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

	sort.Slice(bars, func(i, j int) bool {
		return bars[i].Date.Before(bars[j].Date)
	})
	return bars, nil
}

func historicalDateRangeUTC(now time.Time, days int) dbn_hist.DateRange {
	end := now.UTC().Truncate(24 * time.Hour)
	return dbn_hist.DateRange{
		Start: end.AddDate(0, 0, -days),
		End:   end,
	}
}

func getHistoricalRangeWithContext(ctx context.Context, apiKey string, params *dbn_hist.SubmitJobParams) ([]byte, error) {
	formData := url.Values{}
	if err := params.ApplyToURLValues(&formData); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://hist.databento.com/v0/timeseries.get_range",
		strings.NewReader(formData.Encode()),
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/octet-stream")
	req.SetBasicAuth(apiKey, "")

	resp, err := histHTTPClient.Do(req) //nolint:gosec // Request URL is a trusted Databento constant; user input is only form-encoded parameters.
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s %s", resp.StatusCode, resp.Status, string(body))
	}
	return body, nil
}

func formatHistoricalSummary(symbol string, days int, bars []HistoricalBar) string {
	if len(bars) == 0 {
		return fmt.Sprintf("No historical data returned for %s in the last %d days.", symbol, days)
	}

	first := bars[0]
	last := bars[len(bars)-1]
	high := bars[0].High
	low := bars[0].Low
	for _, bar := range bars[1:] {
		high = math.Max(high, bar.High)
		low = math.Min(low, bar.Low)
	}

	change := 0.0
	if first.Close != 0 {
		change = (last.Close - first.Close) / first.Close * 100
	}
	return fmt.Sprintf(
		"%s %dd (%s to %s)\nClose: $%.2f\nReturn: %.2f%%\nRange: $%.2f - $%.2f",
		symbol,
		days,
		first.Date.Format("2006-01-02"),
		last.Date.Format("2006-01-02"),
		last.Close,
		change,
		low,
		high,
	)
}

func renderHistoricalChartPNG(symbol string, days int, bars []HistoricalBar) ([]byte, error) {
	values := make([]float64, 0, len(bars))
	labels := make([]string, 0, len(bars))
	for _, bar := range bars {
		values = append(values, bar.Close)
		labels = append(labels, bar.Date.Format("01-02"))
	}

	p, err := charts.LineRender(
		[][]float64{values},
		charts.TitleTextOptionFunc(fmt.Sprintf("%s %dD Close", symbol, days)),
		charts.LegendLabelsOptionFunc([]string{symbol}),
		charts.XAxisLabelsOptionFunc(labels),
		func(opt *charts.ChartOption) {
			opt.Symbol = charts.SymbolCircle
			opt.LineStrokeWidth = 1.2
			opt.ValueFormatter = func(f float64) string {
				return fmt.Sprintf("%.2f", f)
			}
		},
	)
	if err != nil {
		return nil, err
	}
	return p.Bytes()
}

func fetchStockQuote(ctx context.Context, symbol string) (*StockQuote, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
	}
	u, err := url.Parse(finnhubBaseURL + "/quote")
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

	resp, err := httpClient.Do(req) //nolint:gosec // URL is built from the trusted finnhubBaseURL constant.
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
	u, err := url.Parse(finnhubBaseURL + "/stock/profile2")
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

	resp, err := httpClient.Do(req) //nolint:gosec // URL is built from the trusted finnhubBaseURL constant.
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

	req, err := http.NewRequestWithContext(ctx, "POST", leetCodeGraphQLURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req) //nolint:gosec // URL is the trusted leetCodeGraphQLURL constant.
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
