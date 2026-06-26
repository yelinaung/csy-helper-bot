// Package bot provides a Telegram bot for stock analysis, code explanation,
// LeetCode assistance, and web search via Exa integration.
package bot

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	appotel "gitlab.com/yelinaung/csy-helper-bot/internal/otel"
)

var (
	httpClient            = &http.Client{Timeout: 10 * time.Second}
	textExplainer         *geminiExplainer
	explainLimiter        *memoryRateLimiter
	stockAnalyzerInstance *stockAnalyzer
	analysisLimiter       *memoryRateLimiter
	botMention            string
	botUserID             int64
	allowedGroups         map[int64]struct{}
)

// wireOTelTransports wraps the package-level HTTP clients' transports with
// otelhttp. It MUST run after appotel.Setup has installed the global meter
// provider: otelhttp binds its client metric instruments eagerly at
// construction time, so wrapping before Setup leaves metrics on the noop
// meter. Called from Run() (which main invokes after Setup). The WrapClient
// helper guards against double-wrapping. Tests replace the whole *http.Client
// vars, bypassing the wrapper; with the noop tracer (test default) nothing is
// emitted.
func wireOTelTransports() {
	appotel.WrapClient(httpClient)
	appotel.WrapClient(histHTTPClient)
	appotel.WrapClient(parallelHTTPClient)
}

// telegramPollTimeout matches the go-telegram/bot default: the long-poll
// getUpdates server timeout is (pollTimeout - 1s) and the HTTP client timeout
// is pollTimeout, leaving a 1s margin.
const telegramPollTimeout = time.Minute

// newTelegramHTTPClient builds the HTTP client the bot library uses for all
// Telegram Bot API calls, instrumented with otelhttp so sends/edits/getFile show
// up as child spans. The long-poll getUpdates request is excluded from tracing
// to avoid emitting a span every poll cycle. Must be called after Setup so
// otelhttp binds its metrics to the real meter provider (Run calls it after
// wireOTelTransports).
func newTelegramHTTPClient() *http.Client {
	transport := appotel.NewHTTPTransportWithFilter(http.DefaultTransport, func(r *http.Request) bool {
		if r == nil || r.URL == nil {
			return true
		}
		// Telegram method is the last path segment: /bot<token>/<method>.
		return !strings.EqualFold(path.Base(r.URL.Path), "getUpdates")
	})
	return &http.Client{
		Timeout:   telegramPollTimeout,
		Transport: transport,
	}
}

// tracer is the package-level tracer for bot operations.
func tracer() trace.Tracer {
	return otel.Tracer("csy-helper-bot/bot")
}

// recordSpanError records err on the span and marks its status ERROR so
// failures are visible in traces. It is a no-op when err is nil.
func recordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// recordRateLimited increments the bot.rate_limited.total counter for the
// given feature ("explain" or "analysis").
func recordRateLimited(ctx context.Context, feature string) {
	appotel.Instruments().RateLimitedTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("feature", feature),
	))
}

const (
	finnhubBaseURL       = "https://finnhub.io/api/v1"
	leetCodeGraphQLURL   = "https://leetcode.com/graphql"
	invalidUsageSymbol   = "invalid usage, use !s SYMBOL or !s SYMBOL 7d|30d|60d|90d"
	dateFormatPattern    = "2006-01-02"
	unexpectedCodeErrMsg = "unexpected status code: %d"
)

func Run() error {
	// .env is loaded by main() before telemetry setup so OTEL_* config is
	// honored; godotenv does not override already-set vars, so there is no
	// need to load it again here.

	// Wire OTel HTTP instrumentation after the caller (main) has run
	// appotel.Setup, so otelhttp binds metrics to the real meter provider.
	wireOTelTransports()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	opts := []bot.Option{
		bot.WithHTTPClient(telegramPollTimeout, newTelegramHTTPClient()),
		bot.WithDefaultHandler(tracingMiddleware(
			"bot.unmatched", "",
			func(ctx context.Context, b *bot.Bot, update *models.Update) {
				logIncomingUpdate(update, false)
				if !enforceChatAccess(ctx, b, update) {
					return
				}
				logUnmatchedMessage(update)
			},
		)),
	}

	b, err := bot.New(token, opts...)
	if err != nil {
		return err
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, startHandler, obs("bot.start", "/start"))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, helpHandler, obs("bot.help", "/help"))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/lc", bot.MatchTypeExact, lcHandler, obs("bot.lc", "/lc"))
	b.RegisterHandler(bot.HandlerTypeMessageText, "!lc", bot.MatchTypeExact, lcHandler, obs("bot.lc", "!lc"))
	b.RegisterHandler(bot.HandlerTypeMessageText, "!s", bot.MatchTypeExact, stockHandler, obs("bot.stock", "!s"))
	b.RegisterHandler(bot.HandlerTypeMessageText, "!s ", bot.MatchTypePrefix, stockHandler, obs("bot.stock", "!s "))
	b.RegisterHandler(bot.HandlerTypeMessageText, "!sa", bot.MatchTypeExact, stockAnalysisHandler, obs("bot.stock_analysis", "!sa"))
	b.RegisterHandler(bot.HandlerTypeMessageText, "!sa ", bot.MatchTypePrefix, stockAnalysisHandler, obs("bot.stock_analysis", "!sa "))

	me, err := b.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch bot profile: %w", err)
	}
	if me.Username != "" {
		botMention = "@" + strings.ToLower(me.Username)
	}
	botUserID = me.ID
	b.RegisterHandlerMatchFunc(shouldHandleAskMention, askHandler, obs("bot.ask", ""))
	b.RegisterHandlerMatchFunc(shouldHandlePhotoAsk, photoAskHandler, obs("bot.photo_ask", ""))
	// Registered after the ask handlers so a message that both mentions the bot
	// and contains an x.com link is answered, not just link-rewritten.
	b.RegisterHandlerMatchFunc(shouldHandleXLink, xLinkHandler, obs("bot.xlink", ""))

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

	initStockAnalyzer()
	analysisLimiter = loadAnalysisRateLimiter()

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

// tracingMiddleware is the outermost handler wrapper. It opens a span named
// after the handler's operation, injects an outcome recorder into the context,
// and records the command counter/duration metrics at span end. The default
// result is "unknown" (never "success") so dashboards never show a false green.
func tracingMiddleware(name string, literal string, next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		ctx, span := tracer().Start(
			ctx, name,
			trace.WithSpanKind(trace.SpanKindInternal),
		)
		defer span.End()

		ctx, recorder := appotel.WithOutcomeRecorder(ctx)
		applyUpdateAttributes(span, name, literal, update)

		start := time.Now()
		next(ctx, b, update)
		elapsed := time.Since(start)

		result := recorder.Result()
		span.SetAttributes(attribute.String("bot.result", result))
		if result == "error" {
			span.SetStatus(codes.Error, "")
		}

		resultAttrs := []attribute.KeyValue{
			attribute.String("bot.command", name),
			attribute.String("bot.result", result),
		}
		inst := appotel.Instruments()
		inst.CommandsTotal.Add(ctx, 1, metric.WithAttributes(resultAttrs...))
		inst.CommandDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(resultAttrs...))
	}
}

// applyUpdateAttributes sets the common bot.* span attributes from an update.
func applyUpdateAttributes(span trace.Span, name string, literal string, update *models.Update) {
	attrs := []attribute.KeyValue{
		attribute.String("bot.command", name),
	}
	if literal != "" {
		attrs = append(attrs, attribute.String("bot.command.literal", literal))
	}
	if update != nil {
		attrs = append(attrs, attribute.Int64("bot.update_id", update.ID))
		if update.Message != nil {
			attrs = append(
				attrs,
				attribute.Int64("bot.chat_id", update.Message.Chat.ID),
				attribute.String("bot.chat_type", string(update.Message.Chat.Type)),
			)
			if update.Message.From != nil {
				attrs = append(attrs, attribute.Int64("bot.user_id", update.Message.From.ID))
			}
		}
	}
	span.SetAttributes(attrs...)
}

// obs composes the tracing middleware (outer) with the request logging
// middleware (inner) and returns a single bot.Middleware. literal is the
// exact command text the user typed (e.g. "/lc" vs "!lc"); it is recorded in
// the bot.command.literal attribute.
func obs(name string, literal string) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return tracingMiddleware(name, literal, requestLoggingMiddleware(next))
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
	appotel.RecordOutcome(ctx, "success")
}

func helpHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	helpText := fmt.Sprintf(`Available commands:
/start - Start the bot
/help - Show this help message
/lc - Get today's LeetCode daily challenge
!s SYMBOL - Get stock price (e.g., !s AAPL)
!s SYMBOL 7d|30d|60d|90d - Get historical chart image (e.g., !s AAPL 7d)
!sa SYMBOL - AI-generated stock analysis, not financial advice (e.g., !sa AAPL)
Mention + question - Ask anything (e.g., @%s what is a mutex?)`, strings.TrimPrefix(botMention, "@"))

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            helpText,
	})
	appotel.RecordOutcome(ctx, "success")
}

// initStockAnalyzer initializes the stock analyzer when all required
// environment variables are configured. This is the sole gate for the
// !sa feature — no separate env check is needed in Run().
func initStockAnalyzer() {
	enabled := strings.ToLower(strings.TrimSpace(os.Getenv("STOCK_ANALYSIS_ENABLED")))
	if enabled != "true" && enabled != "1" {
		log.Info().Msg("Stock analysis disabled (STOCK_ANALYSIS_ENABLED not set to true/1)")
		return
	}

	geminiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if geminiKey == "" {
		log.Warn().Msg("Stock analysis disabled: GEMINI_API_KEY not configured")
		return
	}

	exaKey := strings.TrimSpace(os.Getenv("EXA_API_KEY"))
	if exaKey == "" {
		log.Warn().Msg("Stock analysis disabled: EXA_API_KEY not configured")
		return
	}

	finnhubKey := strings.TrimSpace(os.Getenv("FINNHUB_API_KEY"))
	if finnhubKey == "" {
		log.Warn().Msg("Stock analysis disabled: FINNHUB_API_KEY not configured")
		return
	}

	model := cmp.Or(
		strings.TrimSpace(os.Getenv("STOCK_ANALYSIS_MODEL")),
		strings.TrimSpace(os.Getenv("GEMINI_MODEL")),
		defaultGeminiModelName,
	)
	timeout, err := loadAnalysisTimeout()
	if err != nil {
		log.Error().Err(err).Msg("Stock analysis disabled: invalid STOCK_ANALYSIS_TIMEOUT_SECONDS")
		return
	}

	maxOutputTokens, err := loadAnalysisMaxOutputTokens()
	if err != nil {
		log.Error().Err(err).Msg("Stock analysis disabled: invalid STOCK_ANALYSIS_MAX_OUTPUT_TOKENS")
		return
	}

	analyzer, err := newStockAnalyzer(context.Background(), geminiKey, model, timeout, maxOutputTokens)
	if err != nil {
		log.Error().Err(err).Msg("Failed to initialize stock analyzer")
		return
	}
	stockAnalyzerInstance = analyzer
	log.Info().Str("model", model).Dur("timeout", timeout).Int32("max_output_tokens", maxOutputTokens).Msg("Stock analyzer initialized")
}

const (
	defaultAnalysisRateLimitCount  = 5
	defaultAnalysisRateLimitWindow = 300 // seconds
)

func loadAnalysisRateLimiter() *memoryRateLimiter {
	limit := defaultAnalysisRateLimitCount
	window := time.Duration(defaultAnalysisRateLimitWindow) * time.Second

	if raw := getenvTrim("STOCK_ANALYSIS_RATE_LIMIT_COUNT"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	if raw := getenvTrim("STOCK_ANALYSIS_RATE_LIMIT_WINDOW_SECONDS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			window = time.Duration(n) * time.Second
		}
	}

	return newMemoryRateLimiter(limit, window)
}

func allowAnalysisRequest(message *models.Message) (bool, time.Duration) {
	if message == nil {
		return false, 0
	}
	if analysisLimiter == nil {
		return true, 0
	}

	var userID int64
	if message.From != nil {
		userID = message.From.ID
	}

	key := buildExplainRateKey(message.Chat.ID, userID)
	return analysisLimiter.allow(key, time.Now())
}
