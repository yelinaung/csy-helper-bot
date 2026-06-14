package bot

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/rs/zerolog/log"
)

// maxXLinksPerMessage caps how many rewritten links the bot echoes back for a
// single message, so a wall of pasted links can't spam the chat.
const maxXLinksPerMessage = 5

// xHostRewrites maps the original tweet host to its embed-friendly proxy. Both
// proxies are the FixTweet project and render identical previews; the mapping
// just keeps the bot's reply visually consistent with the link the user posted.
var xHostRewrites = map[string]string{
	"x.com":       "fixupx.com",
	"twitter.com": "fxtwitter.com",
}

// xLinkRegexp finds candidate x.com / twitter.com URLs in free-form text. The
// trailing run is greedy; callers trim stray punctuation and validate the path.
var xLinkRegexp = regexp.MustCompile(`(?i)https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/\S+`)

// xStatusPathRegexp matches the /status/<id> segment that identifies a tweet,
// so profile and search links (which don't benefit from a preview fix) are
// ignored.
var xStatusPathRegexp = regexp.MustCompile(`(?i)/status/[0-9]+`)

// shouldHandleXLink reports whether a plain text message contains at least one
// rewritable tweet link. It is registered after the ask handlers so messages
// that also mention the bot are handled there instead.
func shouldHandleXLink(update *models.Update) bool {
	if update == nil || update.Message == nil {
		return false
	}
	if strings.TrimSpace(update.Message.Text) == "" {
		return false
	}
	return len(extractFixedXLinks(update.Message.Text)) > 0
}

func xLinkHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	links := extractFixedXLinks(update.Message.Text)
	if len(links) == 0 {
		return
	}

	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            strings.Join(links, "\n"),
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
	if err != nil {
		log.Warn().
			Err(err).
			Int64("chat_id", update.Message.Chat.ID).
			Int("link_count", len(links)).
			Msg("Failed to send rewritten x.com links")
	}
}

// extractFixedXLinks pulls every tweet URL out of text and returns the
// embed-friendly rewrites, deduplicated and capped. Non-tweet links and
// anything that fails to parse are skipped.
func extractFixedXLinks(text string) []string {
	matches := xLinkRegexp.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	fixed := make([]string, 0, len(matches))
	for _, raw := range matches {
		// The greedy \S+ can swallow trailing punctuation from prose like
		// "see https://x.com/a/status/1)." — strip the common offenders.
		raw = strings.TrimRight(raw, ".,!?;:)]}>\"'")

		rewritten, ok := rewriteXLink(raw)
		if !ok {
			continue
		}
		if _, dup := seen[rewritten]; dup {
			continue
		}
		seen[rewritten] = struct{}{}
		fixed = append(fixed, rewritten)
		if len(fixed) == maxXLinksPerMessage {
			break
		}
	}

	return fixed
}

// rewriteXLink converts a single x.com/twitter.com tweet URL to its proxy form,
// dropping query and fragment for a clean preview. It returns false for URLs
// that are not parseable, not a known host, or not a tweet status link.
func rewriteXLink(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}

	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "mobile.")

	newHost, ok := xHostRewrites[host]
	if !ok {
		return "", false
	}
	if !xStatusPathRegexp.MatchString(u.Path) {
		return "", false
	}

	return "https://" + newHost + u.Path, true
}
