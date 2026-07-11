package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	appotel "gitlab.com/yelinaung/csy-helper-bot/internal/otel"
)

func initGeminiExplainer() (*geminiExplainer, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("GEMINI_API_KEY not configured")
	}

	model := os.Getenv("GEMINI_MODEL")
	timeout, err := loadGeminiTimeout()
	if err != nil {
		return nil, err
	}
	return newGeminiExplainer(context.Background(), apiKey, model, timeout)
}

func loadGeminiTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("GEMINI_TIMEOUT_SECONDS"))
	if raw == "" {
		return defaultExplainTimeout, nil
	}

	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid GEMINI_TIMEOUT_SECONDS %q: %w", raw, err)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("invalid GEMINI_TIMEOUT_SECONDS %q: must be greater than 0", raw)
	}

	return time.Duration(seconds) * time.Second, nil
}

func askHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if textExplainer == nil {
		appotel.RecordOutcome(ctx, "not_configured")
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Ask feature is not configured. Please set GEMINI_API_KEY.",
		})
		return
	}

	question := extractAskQuestion(update.Message)
	quoted := extractQuotedText(update.Message)
	repliedPhoto := extractRepliedPhoto(update.Message)
	if question == "" && quoted == "" && repliedPhoto == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text: fmt.Sprintf(
				`Send "%q your question here", or reply to a message with "%q" (optionally followed by a question) to ask about it.`,
				botMention, botMention,
			),
		})
		return
	}

	allowed, retryAfter := allowExplainRequest(update.Message)
	if !allowed {
		appotel.RecordOutcome(ctx, "rate_limited")
		recordRateLimited(ctx, "explain")
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

	respondInBurmese := shouldRespondInBurmese(update.Message.Text, quoted)

	explanation, err := answerAskQuestion(ctx, b, repliedPhoto, quoted, question, respondInBurmese)
	if errors.Is(err, errAskPhotoDownload) {
		appotel.RecordOutcome(ctx, "error")
		log.Error().Err(err).Msg("Failed to download replied photo")
		sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr,
			"Failed to download the replied image. Please try again.")
		return
	}
	if err != nil {
		// A safety block is an expected verdict from Gemini, not an application
		// fault — log it at WARN so genuine failures stay visible at ERR.
		if errors.Is(err, ErrExplainBlocked) {
			appotel.RecordOutcome(ctx, "blocked")
			log.Warn().Err(err).Msg("Ask question blocked by safety filters")
		} else {
			appotel.RecordOutcome(ctx, "error")
			log.Error().Err(err).Msg("Failed to answer ask question")
		}
		sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, explainErrorToUserText(err))
		return
	}

	appotel.RecordOutcome(ctx, "success")
	sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, explanation)
}

var errAskPhotoDownload = errors.New("download replied photo")

func answerAskQuestion(
	ctx context.Context,
	b *bot.Bot,
	photo *models.PhotoSize,
	quoted, question string,
	respondInBurmese bool,
) (string, error) {
	if photo == nil {
		return answerTextQuestion(ctx, quoted, question, respondInBurmese)
	}

	imageBytes, mimeType, err := downloadTelegramPhoto(ctx, b, photo.FileID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errAskPhotoDownload, err)
	}
	if quoted != "" {
		return textExplainer.explainWithTextAndImage(ctx, quoted, imageBytes, mimeType, question, respondInBurmese)
	}
	return textExplainer.explainWithImage(ctx, imageBytes, mimeType, question, respondInBurmese)
}

// answerTextQuestion answers a text-only ask request. When Parallel search is
// configured and Gemini judges the question to need fresh web data, the answer
// is grounded in Parallel Search excerpts. Every search-path failure falls
// back to the plain Gemini answer so users never see a search error.
func answerTextQuestion(ctx context.Context, quoted string, question string, respondInBurmese bool) (string, error) {
	if searcher := newParallelSearcher(); searcher != nil {
		plan, err := textExplainer.classifySearchNeed(ctx, quoted, question)
		switch {
		case err != nil:
			log.Warn().Err(err).Msg("Search-need classification failed; answering without web search")
		case plan.NeedsSearch:
			log.Info().
				Str("objective", plan.Objective).
				Strs("search_queries", plan.SearchQueries).
				Msg("Question needs fresh information; running Parallel search")
			results, searchErr := searcher.search(ctx, plan.Objective, plan.SearchQueries)
			switch {
			case searchErr != nil:
				log.Warn().Err(searchErr).Msg("Parallel search failed; answering without web search")
			case len(results) > 0:
				return textExplainer.explainWithSearchResults(ctx, quoted, question, results, respondInBurmese)
			}
		}
	}

	return textExplainer.explainWithLanguage(ctx, quoted, question, respondInBurmese)
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

func explainErrorToUserText(err error) string {
	switch {
	case errors.Is(err, ErrExplainTimeout):
		return "Answer timed out. Please try again."
	case errors.Is(err, ErrExplainBlocked):
		return "I can't answer that request."
	case errors.Is(err, ErrImageTooLarge):
		return "The image is too large to analyze."
	case errors.Is(err, ErrInvalidImageType):
		return "The image type is not supported."
	default:
		return "Failed to answer your question. Please try again later."
	}
}

func extractQuotedText(message *models.Message) string {
	if message == nil {
		return ""
	}

	// Prefer the explicitly highlighted Quote snippet over the full replied
	// message, since it represents the user's specific point of interest.
	if message.Quote != nil {
		if quoteText := strings.TrimSpace(message.Quote.Text); quoteText != "" {
			return quoteText
		}
	}

	if message.ReplyToMessage != nil {
		if txt := strings.TrimSpace(message.ReplyToMessage.Text); txt != "" {
			return txt
		}
		if caption := strings.TrimSpace(message.ReplyToMessage.Caption); caption != "" {
			return caption
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

	if strings.TrimSpace(suffix) != "" {
		return true
	}

	// Bare @bot mention is also valid when it replies to / quotes a message —
	// the quoted text becomes the thing to explain.
	return extractQuotedText(update.Message) != "" || extractRepliedPhoto(update.Message) != nil
}

func extractAskQuestion(message *models.Message) string {
	if message == nil || botMention == "" {
		return ""
	}

	mention, suffix, ok := extractMentionAndSuffix(message)
	if !ok || !strings.EqualFold(mention, botMention) {
		return ""
	}

	return stripAskPrefix(suffix)
}

func stripAskPrefix(suffix string) string {
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

	// Search case-insensitively without lowercasing the whole text.
	// strings.ToLower is not byte-length-preserving (e.g. ẞ U+1E9E → ß
	// shrinks 3 bytes to 2; İ U+0130 → i̇ grows 2 bytes to 3), so byte
	// offsets found in lowerText do not map back to text. Walk text
	// rune-by-rune and compare lowercased runes against the lowercased
	// mention, recording byte offsets in the original string.
	lowerMention := strings.ToLower(targetMention)
	textBytes := []byte(text)

	for startByte := 0; startByte < len(textBytes); {
		// Try to match the mention starting at this byte offset.
		endByte, matched := matchMentionAt(textBytes, startByte, lowerMention)
		if matched && hasMentionBoundaries(textBytes, startByte, endByte) {
			return string(textBytes[startByte:endByte]), string(textBytes[endByte:]), true
		}
		// Advance one rune so the next attempt starts at the next rune
		// boundary. Invalid UTF-8 advances one byte.
		_, size := utf8.DecodeRune(textBytes[startByte:])
		startByte += size
	}

	return "", "", false
}

// matchMentionAt reports whether text[startByte:] begins with targetMention
// (case-insensitive) and returns the byte offset one past the end of the
// match. It lowercases one rune at a time from text and compares against
// the pre-lowercased mention runes, so byte offsets always refer to the
// original text.
func matchMentionAt(text []byte, startByte int, lowerMention string) (endByte int, ok bool) {
	textPos := startByte
	mentionPos := 0
	for mentionPos < len(lowerMention) {
		if textPos >= len(text) {
			return 0, false
		}
		// Lowercase one rune from text.
		r, size := utf8.DecodeRune(text[textPos:])
		lowerR := unicode.ToLower(r)
		// Lowercase one rune from the mention at the current position.
		mr, mSize := utf8.DecodeRuneInString(lowerMention[mentionPos:])
		if lowerR != mr {
			return 0, false
		}
		textPos += size
		mentionPos += mSize
	}
	return textPos, true
}

func hasMentionBoundaries(text []byte, start, end int) bool {
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

// extractPhoto returns the highest-resolution variant from a message's photo
// array. Telegram sends multiple sizes; the last element is the largest.
func extractPhoto(message *models.Message) *models.PhotoSize {
	if message == nil || len(message.Photo) == 0 {
		return nil
	}
	return &message.Photo[len(message.Photo)-1]
}

func extractRepliedPhoto(message *models.Message) *models.PhotoSize {
	if message == nil || message.ReplyToMessage == nil {
		return nil
	}
	return extractPhoto(message.ReplyToMessage)
}

func downloadTelegramPhoto(ctx context.Context, b *bot.Bot, fileID string) (image []byte, mimeType string, err error) {
	ctx, span := tracer().Start(
		ctx, "telegram.download_photo",
		trace.WithAttributes(attribute.String("telegram.file_id", truncateFileID(fileID))),
	)
	defer func() {
		recordSpanError(span, err)
		span.End()
	}()

	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, "", fmt.Errorf("get file %s: %w", fileID, err)
	}
	if file.FilePath == "" {
		return nil, "", errors.New("empty file path from Telegram")
	}

	downloadURL := b.FileDownloadLink(file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create download request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download photo: %w", sanitizeHTTPClientError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download photo: got status %d", resp.StatusCode)
	}

	imageBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read photo body: %w", err)
	}

	mimeType = http.DetectContentType(imageBytes)
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = mime.TypeByExtension(path.Ext(file.FilePath))
	}
	if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
		mimeType = "image/jpeg"
	}

	return imageBytes, mimeType, nil
}

func shouldHandlePhotoAsk(update *models.Update) bool {
	if update == nil || update.Message == nil {
		return false
	}
	if botMention == "" {
		return false
	}
	if len(update.Message.Photo) == 0 {
		return false
	}

	return containsMention(update.Message.Caption, botMention)
}

func photoAskHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if textExplainer == nil {
		appotel.RecordOutcome(ctx, "not_configured")
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Ask feature is not configured. Please set GEMINI_API_KEY.",
		})
		return
	}

	allowed, retryAfter := allowExplainRequest(update.Message)
	if !allowed {
		appotel.RecordOutcome(ctx, "rate_limited")
		recordRateLimited(ctx, "photo_explain")
		var userID int64
		if update.Message.From != nil {
			userID = update.Message.From.ID
		}
		log.Warn().
			Int64("chat_id", update.Message.Chat.ID).
			Int64("user_id", userID).
			Dur("retry_after", retryAfter).
			Msg("Photo ask request rate limited")
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
			Msg("Failed to send thinking message for photo ask request")
	}

	photo := extractPhoto(update.Message)
	if photo == nil {
		appotel.RecordOutcome(ctx, "error")
		sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr,
			"Failed to process the image. Please try again.")
		return
	}

	imageBytes, mimeType, downloadErr := downloadTelegramPhoto(ctx, b, photo.FileID)
	if downloadErr != nil {
		appotel.RecordOutcome(ctx, "error")
		log.Error().Err(downloadErr).Msg("Failed to download photo")
		sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr,
			"Failed to download the image. Please try again.")
		return
	}

	question := extractPhotoAskQuestion(update.Message)
	quoted := extractQuotedText(update.Message)
	respondInBurmese := shouldRespondInBurmese(update.Message.Caption,
		update.Message.Text, quoted)

	var explanation string
	var explainErr error

	if quoted != "" {
		explanation, explainErr = textExplainer.explainWithTextAndImage(ctx, quoted,
			imageBytes, mimeType, question, respondInBurmese)
	} else {
		explanation, explainErr = textExplainer.explainWithImage(ctx, imageBytes,
			mimeType, question, respondInBurmese)
	}

	if explainErr != nil {
		if errors.Is(explainErr, ErrExplainBlocked) {
			appotel.RecordOutcome(ctx, "blocked")
			log.Warn().Err(explainErr).Msg("Photo ask question blocked by safety filters")
		} else {
			appotel.RecordOutcome(ctx, "error")
			log.Error().Err(explainErr).Msg("Failed to answer photo ask question")
		}
		sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, explainErrorToUserText(explainErr))
		return
	}

	appotel.RecordOutcome(ctx, "success")
	sendOrEditExplainResult(ctx, b, update, thinkingMsg, thinkingErr, explanation)
}

func extractPhotoAskQuestion(message *models.Message) string {
	if message == nil || botMention == "" {
		return ""
	}

	caption := message.Caption
	if strings.TrimSpace(caption) == "" {
		return ""
	}

	for _, entity := range message.CaptionEntities {
		if entity.Type != models.MessageEntityTypeMention {
			continue
		}
		mention, suffix, ok := mentionAndSuffixAtEntity(caption, &entity)
		if ok && strings.EqualFold(mention, botMention) {
			return stripAskPrefix(suffix)
		}
	}

	mention, suffix, ok := mentionAndSuffixFromText(caption, botMention)
	if !ok || !strings.EqualFold(mention, botMention) {
		return ""
	}

	return stripAskPrefix(suffix)
}

func containsMention(text, targetMention string) bool {
	if strings.TrimSpace(text) == "" || targetMention == "" {
		return false
	}
	_, _, ok := mentionAndSuffixFromText(text, targetMention)
	return ok
}

// truncateFileID limits a Telegram file_id to a short prefix so the attribute
// value stays low-cardinality and does not leak the full opaque identifier.
func truncateFileID(fileID string) string {
	const maxLen = 16
	if len(fileID) <= maxLen {
		return fileID
	}
	return fileID[:maxLen] + "..."
}
