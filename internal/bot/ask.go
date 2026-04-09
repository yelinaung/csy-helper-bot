package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/rs/zerolog/log"
)

func initGeminiExplainer() (*geminiExplainer, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("GEMINI_API_KEY not configured")
	}

	model := os.Getenv("GEMINI_MODEL")
	return newGeminiExplainer(context.Background(), apiKey, model)
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
	quoted := extractQuotedText(update.Message)
	if question == "" && quoted == "" {
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
	explanation, err := textExplainer.explainWithLanguage(ctx, quoted, question, respondInBurmese)
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
	return extractQuotedText(update.Message) != ""
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
