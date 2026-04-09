package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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
	httpClient     = &http.Client{Timeout: 10 * time.Second}
	textExplainer  *geminiExplainer
	explainLimiter *memoryRateLimiter
	botMention     string
	botUserID      int64
	allowedGroups  map[int64]struct{}
)

const (
	finnhubBaseURL       = "https://finnhub.io/api/v1"
	leetCodeGraphQLURL   = "https://leetcode.com/graphql"
	invalidUsageSymbol   = "invalid usage, use !s SYMBOL or !s SYMBOL 7d|30d"
	dateFormatPattern    = "2006-01-02"
	unexpectedCodeErrMsg = "unexpected status code: %d"
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
		model := strings.TrimSpace(textExplainer.model)
		if model == "" {
			model = defaultGeminiModelName
		}
		timeout := textExplainer.explainTimeout
		if timeout <= 0 {
			timeout = defaultExplainTimeout
		}
		log.Info().
			Str("model", model).
			Dur("timeout", timeout).
			Msg("Gemini explainer initialized")
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
