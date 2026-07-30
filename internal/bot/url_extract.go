package bot

import (
	"net"
	"net/url"
	"strings"

	"github.com/go-telegram/bot/models"
)

// defaultExtractObjective is used when the stripped question has no text of
// its own — a bare link, or a bare @bot mention replying to a link-bearing
// message. Parallel must never receive an empty objective.
const defaultExtractObjective = "Summarize the key points of this page."

// maxExtractRawURLLen bounds the raw URL text accepted before parsing, so a
// pathological pasted string cannot blow up downstream processing.
const maxExtractRawURLLen = 2048

// extractQuestionURLs finds URLs relevant to an ask request: those in the
// user's own question and, unless the quoted message is the bot's own,
// those in the quoted/replied-to text. It returns up to maxURLs normalized,
// deduplicated URLs (question-side first) plus the question with every
// literal URL token it found removed, for use as the extract objective.
func extractQuestionURLs(message *models.Message, question, quoted string, maxURLs int) (urls []string, strippedQuestion string) {
	if maxURLs <= 0 {
		maxURLs = defaultExtractMaxURLs
	}

	var candidates []string
	// questionURLTokens holds only the raw substrings that appear verbatim
	// in `question` — url-type entity text and scanTextURLs hits — so they
	// can be stripped out of the objective. A text_link entity's target URL
	// is a valid extraction candidate but is deliberately excluded here: its
	// visible anchor text (e.g. "check this out") is not a URL and is part
	// of what the user actually asked, so it must stay in the objective.
	var questionURLTokens []string

	// The user's own question. message.Entities offsets are relative to the
	// *whole* message.Text, which can carry content before the mention that
	// extractAskQuestion never treats as the question (e.g. a URL pasted
	// ahead of "@bot explain this"); only entities inside the extracted
	// question suffix are considered, so that prefix content never
	// consumes the URL cap or gets billed.
	if message != nil {
		if suffixStart, ok := questionSuffixStart(message); ok {
			suffixEntities := entitiesFromByteOffset(message.Text, message.Entities, suffixStart)
			entityURLs := urlEntityStrings(message.Text, suffixEntities)
			candidates = append(candidates, entityURLs...)
			candidates = append(candidates, textLinkEntityStrings(suffixEntities)...)
			questionURLTokens = append(questionURLTokens, entityURLs...)
		}
	}
	scannedQuestionURLs := scanTextURLs(question)
	candidates = append(candidates, scannedQuestionURLs...)
	questionURLTokens = append(questionURLTokens, scannedQuestionURLs...)

	// The quoted side, harvested only from the single text source
	// extractQuotedText actually selected (Quote > reply text > reply
	// caption) — never the full replied message, and never when the quote
	// is the bot's own text (its citation links shouldn't be re-billed).
	if !isQuotedFromBot(message) {
		quoteText, quoteEntities := selectedQuoteSource(message)
		candidates = append(candidates, urlsFromEntities(quoteText, quoteEntities)...)
		candidates = append(candidates, scanTextURLs(quoted)...)
	}

	seen := make(map[string]struct{}, len(candidates))
	for _, raw := range candidates {
		normalized, ok := normalizeExtractURL(raw)
		if !ok {
			continue
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		urls = append(urls, normalized)
		if len(urls) >= maxURLs {
			break
		}
	}

	return urls, stripKnownURLTokens(question, questionURLTokens)
}

// extractObjectiveFor turns a stripped question into a Parallel Extract
// objective, defaulting when it's empty — a bare-link question, or a bare
// @bot mention replying to a link-bearing message both strip down to
// nothing, and Parallel must never receive an empty objective.
func extractObjectiveFor(strippedQuestion string) string {
	if strings.TrimSpace(strippedQuestion) == "" {
		return defaultExtractObjective
	}
	return strippedQuestion
}

// questionSuffixStart returns the byte offset in message.Text where the
// question begins — the position right after the bot mention — mirroring
// extractAskQuestion/extractMentionAndSuffix's own precedence (an entity
// match first, then a text scan) without re-deriving the suffix string
// itself. Entities starting before this offset belong to content Telegram
// tagged ahead of the mention and are not part of what the user asked.
func questionSuffixStart(message *models.Message) (int, bool) {
	if message == nil || botMention == "" {
		return 0, false
	}
	text := message.Text
	if strings.TrimSpace(text) == "" {
		return 0, false
	}

	for _, entity := range message.Entities {
		if entity.Type != models.MessageEntityTypeMention {
			continue
		}
		start, end, ok := utf16EntityRangeToByteRange(text, entity.Offset, entity.Length)
		if !ok {
			continue
		}
		if strings.EqualFold(text[start:end], botMention) {
			return end, true
		}
	}

	_, suffix, ok := mentionAndSuffixFromText(text, botMention)
	if !ok {
		return 0, false
	}
	return len(text) - len(suffix), true
}

// entitiesFromByteOffset returns only the entities whose Telegram UTF-16
// range maps to a byte range starting at or after minStart in text.
func entitiesFromByteOffset(text string, entities []models.MessageEntity, minStart int) []models.MessageEntity {
	var filtered []models.MessageEntity
	for _, e := range entities {
		start, _, ok := utf16EntityRangeToByteRange(text, e.Offset, e.Length)
		if !ok || start < minStart {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// selectedQuoteSource returns the text and entities of whichever source
// extractQuotedText would select for the same message, mirroring its
// precedence exactly: a manually selected Quote, then the replied message's
// text, then its caption. Harvesting only this source (not the full
// replied message) matches what the flow actually sends to Gemini as
// "quoted" text.
func selectedQuoteSource(message *models.Message) (text string, entities []models.MessageEntity) {
	if message == nil {
		return "", nil
	}

	if message.Quote != nil && strings.TrimSpace(message.Quote.Text) != "" {
		return message.Quote.Text, message.Quote.Entities
	}

	if message.ReplyToMessage != nil {
		if strings.TrimSpace(message.ReplyToMessage.Text) != "" {
			return message.ReplyToMessage.Text, message.ReplyToMessage.Entities
		}
		if strings.TrimSpace(message.ReplyToMessage.Caption) != "" {
			return message.ReplyToMessage.Caption, message.ReplyToMessage.CaptionEntities
		}
	}

	return "", nil
}

// urlsFromEntities returns the raw URL text of every url/text_link entity:
// url-type entities' literal substring plus text_link entities' target URL.
func urlsFromEntities(text string, entities []models.MessageEntity) []string {
	found := urlEntityStrings(text, entities)
	found = append(found, textLinkEntityStrings(entities)...)
	return found
}

// urlEntityStrings returns the literal substring of every url-type entity —
// text that appears verbatim in the surrounding message, unlike a
// text_link's target URL. Entity offsets are UTF-16 code units per the
// Telegram Bot API; conversion to byte ranges is delegated to
// utf16EntityRangeToByteRange, which already fails closed (returns
// ok=false) on out-of-range or empty-text input.
func urlEntityStrings(text string, entities []models.MessageEntity) []string {
	var found []string
	for _, e := range entities {
		if e.Type != models.MessageEntityTypeURL {
			continue
		}
		start, end, ok := utf16EntityRangeToByteRange(text, e.Offset, e.Length)
		if !ok {
			continue
		}
		found = append(found, text[start:end])
	}
	return found
}

// textLinkEntityStrings returns the target URL of every text_link entity.
func textLinkEntityStrings(entities []models.MessageEntity) []string {
	var found []string
	for _, e := range entities {
		if e.Type != models.MessageEntityTypeTextLink {
			continue
		}
		if u := strings.TrimSpace(e.URL); u != "" {
			found = append(found, u)
		}
	}
	return found
}

// scanTextURLs is the fallback for messages arriving without entities: it
// picks whitespace-delimited tokens starting with http:// or https://,
// after normalizeURLToken strips punctuation the surrounding sentence
// attached rather than punctuation that's part of the URL itself. It
// deliberately does not attempt bare-domain detection — that is only
// reliable via Telegram's own entities.
func scanTextURLs(text string) []string {
	var found []string
	for field := range strings.FieldsSeq(text) {
		trimmed := normalizeURLToken(field)
		if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
			found = append(found, trimmed)
		}
	}
	return found
}

// normalizeURLToken strips sentence punctuation a URL never legitimately
// ends with, plus any leading/trailing bracket left unbalanced by the
// token itself. A wrapping bracket the sentence added — "(https://x.com)"
// — is removed entirely; a bracket pair that is genuinely part of the URL's
// own path — Wikipedia's .../Function_(mathematics) — survives, because
// its open/close counts within the token stay balanced.
func normalizeURLToken(field string) string {
	// A leading wrapper (e.g. "(" in "(https://example.com)") never
	// legitimately precedes a URL's scheme, so strip it before the scheme
	// even starts.
	for len(field) > 0 &&
		!strings.HasPrefix(field, "http://") && !strings.HasPrefix(field, "https://") &&
		strings.ContainsRune("([{<\"'", rune(field[0])) {
		field = field[1:]
	}

	field = strings.TrimRight(field, ".,!?;:\"'")

	for _, pair := range [...][2]byte{{'(', ')'}, {'[', ']'}, {'{', '}'}, {'<', '>'}} {
		open, closeCh := pair[0], pair[1]
		for len(field) > 0 && field[len(field)-1] == closeCh {
			if strings.Count(field, string(closeCh)) <= strings.Count(field, string(open)) {
				break
			}
			field = field[:len(field)-1]
		}
	}

	return field
}

// stripKnownURLTokens removes whitespace-delimited tokens from text whose
// normalized form exactly matches one of knownURLs. It is used to build the
// extract objective, and deliberately reuses normalizeURLToken — the same
// normalization scanTextURLs used to find these tokens in the first place —
// so a token is only removed here if it was actually counted as one of the
// URLs sent to Parallel (a scheme-less bare domain from an entity, or a
// punctuation-wrapped fallback token, are both matched this way; the old
// http(s)-prefix-only check missed both).
func stripKnownURLTokens(text string, knownURLs []string) string {
	if len(knownURLs) == 0 {
		return strings.TrimSpace(text)
	}
	known := make(map[string]struct{}, len(knownURLs))
	for _, u := range knownURLs {
		known[u] = struct{}{}
	}

	var kept []string
	for field := range strings.FieldsSeq(text) {
		if _, ok := known[normalizeURLToken(field)]; ok {
			continue
		}
		kept = append(kept, field)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

// normalizeExtractURL validates and normalizes a candidate URL string.
// Scheme-less input (bare domains from entities) is assumed https://; an
// http-only site reached this way will fail extraction and simply be
// skipped like any dead link, same as scanTextURLs's failure mode — users
// can paste an explicit http:// URL to bypass this.
func normalizeExtractURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxExtractRawURLLen {
		return "", false
	}

	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}

	u, err := url.Parse(candidate)
	if err != nil {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	host := u.Hostname()
	if host == "" || isLocalOrPrivateHost(host) {
		return "", false
	}

	// Lowercase the host so the same page referenced with different host
	// casing (e.g. from separate Telegram entities) dedupes to one URL —
	// hosts are case-insensitive per RFC 3986, unlike paths and queries.
	if lowerHost := strings.ToLower(u.Host); lowerHost != u.Host {
		u.Host = lowerHost
		candidate = u.String()
	}

	return candidate, true
}

// isLocalOrPrivateHost reports whether host resolves to localhost or a
// private/link-local address. Extraction is fetched by Parallel's own
// infrastructure, not this process, so this is not an SSRF guard; it just
// avoids paying for and confusingly failing on URLs that could never
// resolve to a real public page.
func isLocalOrPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
