package bot

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestExtractQuestionURLs_EntityInQuestion(t *testing.T) {
	prevMention := botMention
	botMention = testBotMention
	defer func() { botMention = prevMention }()

	link := "https://entity.example.net/pricing"
	text := testBotMention + " " + link + " what are the plan prices?"
	message := &models.Message{
		Text: text,
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeMention, Offset: 0, Length: len(testBotMention)},
			{Type: models.MessageEntityTypeURL, Offset: len(testBotMention) + 1, Length: len(link)},
		},
	}
	question := link + " what are the plan prices?"

	urls, stripped := extractQuestionURLs(message, question, "", 3)

	if len(urls) != 1 || urls[0] != link {
		t.Fatalf("urls = %v, want [%s]", urls, link)
	}
	if stripped != "what are the plan prices?" {
		t.Errorf("strippedQuestion = %q", stripped)
	}
}

func TestExtractQuestionURLs_TextLinkEntityInQuestion(t *testing.T) {
	prevMention := botMention
	botMention = testBotMention
	defer func() { botMention = prevMention }()

	anchor := "check this out"
	text := testBotMention + " " + anchor + " what does it say?"
	message := &models.Message{
		Text: text,
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeMention, Offset: 0, Length: len(testBotMention)},
			{Type: models.MessageEntityTypeTextLink, Offset: len(testBotMention) + 1, Length: len(anchor), URL: "https://textlink.example.net/target"},
		},
	}
	question := anchor + " what does it say?"

	urls, stripped := extractQuestionURLs(message, question, "", 3)

	if len(urls) != 1 || urls[0] != "https://textlink.example.net/target" {
		t.Fatalf("urls = %v, want [https://textlink.example.net/target]", urls)
	}
	// The anchor text is part of what the user actually asked and is not
	// itself a URL, so it must survive into the objective.
	if stripped != anchor+" what does it say?" {
		t.Errorf("strippedQuestion = %q, want anchor text preserved", stripped)
	}
}

// TestExtractQuestionURLs_EntitiesBeforeMentionIgnored covers content
// Telegram tagged ahead of the bot mention (e.g. a link pasted before
// "@bot"), which extractAskQuestion never treats as the question. Such
// entities must not consume the URL cap or be billed.
func TestExtractQuestionURLs_EntitiesBeforeMentionIgnored(t *testing.T) {
	prevMention := botMention
	botMention = testBotMention
	defer func() { botMention = prevMention }()

	prefixLink := "https://prefix.example.net/ignored"
	text := prefixLink + " " + testBotMention + " explain this"
	message := &models.Message{
		Text: text,
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeURL, Offset: 0, Length: len(prefixLink)},
			{Type: models.MessageEntityTypeMention, Offset: len(prefixLink) + 1, Length: len(testBotMention)},
		},
	}
	// As askHandler would produce it: only the text after the mention.
	question := "explain this"

	urls, stripped := extractQuestionURLs(message, question, "", 3)

	if len(urls) != 0 {
		t.Fatalf("urls = %v, want none (entity lives before the mention)", urls)
	}
	if stripped != "explain this" {
		t.Errorf("strippedQuestion = %q", stripped)
	}
}

// TestExtractQuestionURLs_BareDomainEntityStrippedFromObjective covers a
// scheme-less URL entity (Telegram tags bare domains like "example.com" as
// url entities): it must both be extracted and be removed from the
// objective, so a bare-link ask still gets the default summary objective
// instead of the domain text itself.
func TestExtractQuestionURLs_BareDomainEntityStrippedFromObjective(t *testing.T) {
	prevMention := botMention
	botMention = testBotMention
	defer func() { botMention = prevMention }()

	domain := "bare-domain.example.net"
	text := testBotMention + " " + domain
	message := &models.Message{
		Text: text,
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeMention, Offset: 0, Length: len(testBotMention)},
			{Type: models.MessageEntityTypeURL, Offset: len(testBotMention) + 1, Length: len(domain)},
		},
	}
	question := domain

	urls, stripped := extractQuestionURLs(message, question, "", 3)

	if len(urls) != 1 || urls[0] != "https://"+domain {
		t.Fatalf("urls = %v, want [https://%s]", urls, domain)
	}
	if stripped != "" {
		t.Errorf("strippedQuestion = %q, want empty so the default objective is used", stripped)
	}
	if got := extractObjectiveFor(stripped); got != defaultExtractObjective {
		t.Errorf("extractObjectiveFor(%q) = %q, want default objective", stripped, got)
	}
}

// TestExtractQuestionURLs_WrappedTokenStrippedFromObjective covers a
// fallback-scanned token wrapped in sentence punctuation, e.g.
// "(https://example.com/a)": the URL is still recognized, and the whole
// wrapped token — not just an http(s):// prefix match — is removed from
// the objective.
func TestExtractQuestionURLs_WrappedTokenStrippedFromObjective(t *testing.T) {
	t.Parallel()

	question := "(https://wrapped.example.net/a) what is this"

	urls, stripped := extractQuestionURLs(nil, question, "", 3)

	if len(urls) != 1 || urls[0] != "https://wrapped.example.net/a" {
		t.Fatalf("urls = %v, want [https://wrapped.example.net/a]", urls)
	}
	if stripped != "what is this" {
		t.Errorf("strippedQuestion = %q", stripped)
	}
}

func TestExtractQuestionURLs_FallbackScanNoEntities(t *testing.T) {
	t.Parallel()

	question := "https://fallback.example.net summarize this"

	urls, stripped := extractQuestionURLs(nil, question, "", 3)

	if len(urls) != 1 || urls[0] != "https://fallback.example.net" {
		t.Fatalf("urls = %v, want [https://fallback.example.net]", urls)
	}
	if stripped != "summarize this" {
		t.Errorf("strippedQuestion = %q", stripped)
	}
}

func TestExtractQuestionURLs_QuoteEntitiesOnlySelectedSource(t *testing.T) {
	t.Parallel()

	quoteText := "check out https://quote.example.net/a for details"
	message := &models.Message{
		Text: "@bot summarize",
		Quote: &models.TextQuote{
			Text: quoteText,
			Entities: []models.MessageEntity{
				{Type: models.MessageEntityTypeURL, Offset: len("check out "), Length: len("https://quote.example.net/a")},
			},
		},
		ReplyToMessage: &models.Message{
			// A different URL living in the full replied message, outside
			// the manually selected quote — must NOT be harvested.
			Text: "intro https://quote.example.net/outside outro " + quoteText,
			Entities: []models.MessageEntity{
				{Type: models.MessageEntityTypeURL, Offset: len("intro "), Length: len("https://quote.example.net/outside")},
			},
		},
	}

	urls, _ := extractQuestionURLs(message, "summarize", quoteText, 3)

	if len(urls) != 1 || urls[0] != "https://quote.example.net/a" {
		t.Fatalf("urls = %v, want only the quoted URL [https://quote.example.net/a]", urls)
	}
}

func TestExtractQuestionURLs_ReplyTextEntitiesWhenNoQuote(t *testing.T) {
	t.Parallel()

	replyText := "see https://reply.example.net/b"
	message := &models.Message{
		Text: "@bot summarize",
		ReplyToMessage: &models.Message{
			Text: replyText,
			Entities: []models.MessageEntity{
				{Type: models.MessageEntityTypeURL, Offset: len("see "), Length: len("https://reply.example.net/b")},
			},
		},
	}

	urls, _ := extractQuestionURLs(message, "summarize", replyText, 3)

	if len(urls) != 1 || urls[0] != "https://reply.example.net/b" {
		t.Fatalf("urls = %v, want [https://reply.example.net/b]", urls)
	}
}

func TestExtractQuestionURLs_ReplyCaptionEntitiesForNonPhotoMedia(t *testing.T) {
	t.Parallel()

	caption := "see https://caption.example.net/c"
	message := &models.Message{
		Text: "@bot summarize",
		ReplyToMessage: &models.Message{
			// No Text (a video/document caption, not a text message).
			Caption: caption,
			CaptionEntities: []models.MessageEntity{
				{Type: models.MessageEntityTypeURL, Offset: len("see "), Length: len("https://caption.example.net/c")},
			},
		},
	}

	urls, _ := extractQuestionURLs(message, "summarize", caption, 3)

	if len(urls) != 1 || urls[0] != "https://caption.example.net/c" {
		t.Fatalf("urls = %v, want [https://caption.example.net/c]", urls)
	}
}

func TestExtractQuestionURLs_BotOwnedQuoteIgnored(t *testing.T) {
	prevBotUserID := botUserID
	botUserID = 42
	defer func() { botUserID = prevBotUserID }()

	replyText := "see https://botquote.example.net/citation"
	message := &models.Message{
		Text: "@bot explain this",
		ReplyToMessage: &models.Message{
			From: &models.User{ID: 42, IsBot: true},
			Text: replyText,
			Entities: []models.MessageEntity{
				{Type: models.MessageEntityTypeURL, Offset: len("see "), Length: len("https://botquote.example.net/citation")},
			},
		},
	}

	urls, _ := extractQuestionURLs(message, "explain this", replyText, 3)

	if len(urls) != 0 {
		t.Fatalf("urls = %v, want none (quoted message is from the bot)", urls)
	}
}

func TestExtractQuestionURLs_DedupAndCap(t *testing.T) {
	t.Parallel()

	question := "https://dedup.example.net/a https://dedup.example.net/a https://dedup.example.net/b https://dedup.example.net/c summarize"

	urls, _ := extractQuestionURLs(nil, question, "", 2)

	want := []string{"https://dedup.example.net/a", "https://dedup.example.net/b"}
	if len(urls) != len(want) {
		t.Fatalf("urls = %v, want %v", urls, want)
	}
	for i, u := range want {
		if urls[i] != u {
			t.Errorf("urls[%d] = %q, want %q", i, urls[i], u)
		}
	}
}

func TestExtractQuestionURLs_QuestionURLsBeforeQuotedURLs(t *testing.T) {
	t.Parallel()

	question := "https://order.example.net/question-link summarize"
	quoted := "https://order.example.net/quoted-link"
	message := &models.Message{
		Text: "@bot " + question,
		ReplyToMessage: &models.Message{
			Text: quoted,
		},
	}

	urls, _ := extractQuestionURLs(message, question, quoted, 3)

	if len(urls) != 2 || urls[0] != "https://order.example.net/question-link" || urls[1] != "https://order.example.net/quoted-link" {
		t.Fatalf("urls = %v, want question link first", urls)
	}
}

func TestExtractQuestionURLs_BareLinkQuestionStripsToEmpty(t *testing.T) {
	t.Parallel()

	question := "https://bare.example.net/article"

	urls, stripped := extractQuestionURLs(nil, question, "", 3)

	if len(urls) != 1 {
		t.Fatalf("urls = %v, want 1", urls)
	}
	if stripped != "" {
		t.Errorf("strippedQuestion = %q, want empty", stripped)
	}
}

func TestExtractObjectiveFor(t *testing.T) {
	t.Parallel()

	if got := extractObjectiveFor("what are the prices?"); got != "what are the prices?" {
		t.Errorf("extractObjectiveFor() = %q", got)
	}
	if got := extractObjectiveFor("   "); got != defaultExtractObjective {
		t.Errorf("extractObjectiveFor() = %q, want default objective", got)
	}
	if got := extractObjectiveFor(""); got != defaultExtractObjective {
		t.Errorf("extractObjectiveFor() = %q, want default objective", got)
	}
}

func TestNormalizeExtractURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"https passthrough", "https://norm.example.net/x", "https://norm.example.net/x", true},
		{"http passthrough", "http://norm.example.net/y", "http://norm.example.net/y", true},
		{"bare domain gets https", "norm-bare.example.net", "https://norm-bare.example.net", true},
		{"bare domain with path", "norm-bare2.example.net/a/b", "https://norm-bare2.example.net/a/b", true},
		{"empty rejected", "", "", false},
		{"whitespace only rejected", "   ", "", false},
		{"scheme without host rejected", "https://", "", false},
		{"unsupported scheme rejected", "ftp://norm.example.net", "", false},
		{"localhost rejected", "http://localhost:8080", "", false},
		{"loopback IP rejected", "http://127.0.0.1", "", false},
		{"private IP rejected", "http://10.0.0.5", "", false},
		{"link-local IP rejected", "http://169.254.1.1", "", false},
		{"IPv6 loopback rejected", "http://[::1]", "", false},
		{"IPv6 loopback with port rejected", "http://[::1]:8080", "", false},
		{"IPv6 private (ULA) rejected", "http://[fd00::1]", "", false},
		{"IPv6 link-local rejected", "http://[fe80::1]", "", false},
		{"overlong URL rejected", "https://norm.example.net/" + strings.Repeat("a", maxExtractRawURLLen), "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := normalizeExtractURL(tt.raw)
			if ok != tt.ok {
				t.Fatalf("normalizeExtractURL(%q) ok = %v, want %v (got %q)", tt.raw, ok, tt.ok, got)
			}
			if ok && got != tt.want {
				t.Errorf("normalizeExtractURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestScanTextURLs(t *testing.T) {
	t.Parallel()

	got := scanTextURLs("check https://scan.example.net/a. and also (https://scan.example.net/b) please")
	want := []string{"https://scan.example.net/a", "https://scan.example.net/b"}
	if len(got) != len(want) {
		t.Fatalf("scanTextURLs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("scanTextURLs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestScanTextURLs_PreservesBalancedPathParens covers Wikipedia-style paths
// where a parenthesis is part of the URL itself, not sentence wrapping: the
// balanced pair inside the path must survive even when the surrounding
// sentence adds its own unbalanced wrapping paren.
func TestScanTextURLs_PreservesBalancedPathParens(t *testing.T) {
	t.Parallel()

	balanced := "https://en.wikipedia.org/wiki/Function_(mathematics)"

	got := scanTextURLs("see " + balanced + " for background")
	if len(got) != 1 || got[0] != balanced {
		t.Fatalf("scanTextURLs() = %v, want [%s] (balanced path parens preserved)", got, balanced)
	}

	gotWrapped := scanTextURLs("(see " + balanced + ").")
	if len(gotWrapped) != 1 || gotWrapped[0] != balanced {
		t.Fatalf("scanTextURLs() = %v, want [%s] (sentence-wrapping punctuation stripped, path parens kept)", gotWrapped, balanced)
	}
}

func TestNormalizeURLToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		field string
		want  string
	}{
		{"plain", "https://token.example.net/a", "https://token.example.net/a"},
		{"trailing period", "https://token.example.net/a.", "https://token.example.net/a"},
		{"fully wrapped in parens", "(https://token.example.net/a)", "https://token.example.net/a"},
		{"wrapped plus trailing period", "(https://token.example.net/a).", "https://token.example.net/a"},
		{"balanced path parens kept", "https://en.wikipedia.org/wiki/Function_(mathematics)", "https://en.wikipedia.org/wiki/Function_(mathematics)"},
		{"balanced path parens plus sentence wrap", "(https://en.wikipedia.org/wiki/Function_(mathematics))", "https://en.wikipedia.org/wiki/Function_(mathematics)"},
		{"angle bracket wrapped", "<https://token.example.net/a>", "https://token.example.net/a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeURLToken(tt.field); got != tt.want {
				t.Errorf("normalizeURLToken(%q) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

func TestStripKnownURLTokens(t *testing.T) {
	t.Parallel()

	got := stripKnownURLTokens("https://strip.example.net/a what are the prices?", []string{"https://strip.example.net/a"})
	if got != "what are the prices?" {
		t.Errorf("stripKnownURLTokens() = %q", got)
	}

	if got := stripKnownURLTokens("https://strip.example.net/a", []string{"https://strip.example.net/a"}); got != "" {
		t.Errorf("stripKnownURLTokens() = %q, want empty", got)
	}

	// A bare domain (as harvested from a scheme-less URL entity) matches by
	// its own literal text, not by the https:// form normalizeExtractURL
	// produces.
	if got := stripKnownURLTokens("strip-bare.example.net what is this", []string{"strip-bare.example.net"}); got != "what is this" {
		t.Errorf("stripKnownURLTokens() = %q, want bare domain stripped", got)
	}

	// A punctuation-wrapped token is matched and removed as a whole, not
	// left as stray punctuation.
	if got := stripKnownURLTokens("(https://strip.example.net/a) what is this", []string{"https://strip.example.net/a"}); got != "what is this" {
		t.Errorf("stripKnownURLTokens() = %q, want wrapped token fully removed", got)
	}

	// Nothing to strip: text passes through trimmed but otherwise intact.
	if got := stripKnownURLTokens("  no urls here  ", nil); got != "no urls here" {
		t.Errorf("stripKnownURLTokens() = %q", got)
	}
}

// TestUrlsFromEntities_EmptyTextSourceWithEntities covers the plan's
// explicitly called-out edge case: entity arrays with non-zero offsets when
// the associated text source is empty (e.g. a quoted image with entities
// but no caption text). utf16EntityRangeToByteRange fails closed for this,
// so it must not panic and must yield no URLs.
func TestUrlsFromEntities_EmptyTextSourceWithEntities(t *testing.T) {
	t.Parallel()

	got := urlsFromEntities("", []models.MessageEntity{
		{Type: models.MessageEntityTypeURL, Offset: 5, Length: 10},
		{Type: models.MessageEntityTypeTextLink, Offset: 0, Length: 3, URL: "https://entities.example.net/target"},
	})

	// The text_link entity's URL field is independent of the empty text and
	// is still returned; only the offset-dependent "url" entity is dropped.
	if len(got) != 1 || got[0] != "https://entities.example.net/target" {
		t.Fatalf("urlsFromEntities() = %v", got)
	}
}
