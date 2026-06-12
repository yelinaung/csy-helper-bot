package bot

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/go-telegram/bot"
)

const generatedMarkdownEscapes = "_*[]()~`>#+-=|{}.!%"

var (
	markdownCodeBlockRE  = regexp.MustCompile("(?s)```(.*?)```")
	markdownInlineCodeRE = regexp.MustCompile("`([^`\n]+)`")
	markdownLinkRE       = regexp.MustCompile(`\[([^\[\]\n]+)\]\((https?://[^)\s]+)\)`)
	markdownBoldRE       = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	markdownItalicRE     = regexp.MustCompile(`\*([^*\n]+)\*|_([^_\n]+)_`)
)

func formatTelegramMarkdown(text string) string {
	// Model output is untrusted: drop invalid UTF-8 and NUL bytes before
	// formatting so Telegram never receives malformed text.
	text = strings.ToValidUTF8(text, "�")
	text = strings.ReplaceAll(text, "\x00", "")
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
		inner := extractAlternation(markdownBoldRE, match)
		if inner == "" {
			return match
		}
		return addToken("*" + bot.EscapeMarkdownUnescaped(inner) + "*")
	})

	normalized = markdownItalicRE.ReplaceAllStringFunc(normalized, func(match string) string {
		inner := extractAlternation(markdownItalicRE, match)
		if inner == "" {
			return match
		}
		return addToken("_" + bot.EscapeMarkdownUnescaped(inner) + "_")
	})

	escaped := bot.EscapeMarkdownUnescaped(normalized)
	for i, token := range slices.Backward(tokens) {
		escaped = strings.ReplaceAll(escaped, fmt.Sprintf("TGMARKTOKEN%dX", i), token)
	}

	return escaped
}

// normalizeGeneratedTelegramMarkdown strips model-generated escape
// backslashes. A backslash run immediately before a MarkdownV2 escape
// character collapses entirely (repeatedly unescaping one level converges to
// exactly that), so the result is idempotent in a single O(n) pass: output
// backslashes only ever precede non-escape characters.
func normalizeGeneratedTelegramMarkdown(text string) string {
	var out strings.Builder
	out.Grow(len(text))

	for i := 0; i < len(text); {
		if text[i] != '\\' {
			out.WriteByte(text[i])
			i++
			continue
		}

		j := i
		for j < len(text) && text[j] == '\\' {
			j++
		}
		if j == len(text) || !strings.ContainsRune(generatedMarkdownEscapes, rune(text[j])) {
			out.WriteString(text[i:j])
		}
		i = j
	}

	return out.String()
}

func plainTelegramMarkdownText(text string) string {
	normalized := normalizeGeneratedTelegramMarkdown(strings.ReplaceAll(text, "\r\n", "\n"))

	normalized = markdownCodeBlockRE.ReplaceAllString(normalized, "$1")
	normalized = markdownInlineCodeRE.ReplaceAllString(normalized, "$1")
	normalized = markdownLinkRE.ReplaceAllString(normalized, "$1 ($2)")
	normalized = markdownBoldRE.ReplaceAllStringFunc(normalized, func(match string) string {
		return extractAlternation(markdownBoldRE, match)
	})
	normalized = markdownItalicRE.ReplaceAllStringFunc(normalized, func(match string) string {
		return extractAlternation(markdownItalicRE, match)
	})

	return strings.TrimSpace(normalized)
}

func extractAlternation(re *regexp.Regexp, match string) string {
	submatches := re.FindStringSubmatch(match)
	if len(submatches) < 3 {
		return ""
	}
	if inner := strings.TrimSpace(submatches[1]); inner != "" {
		return inner
	}
	return strings.TrimSpace(submatches[2])
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
		`[`, `\[`,
		`]`, `\]`,
	)
	return replacer.Replace(text)
}
