package bot

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/go-telegram/bot"
)

var (
	markdownCodeBlockRE  = regexp.MustCompile("(?s)```(.*?)```")
	markdownInlineCodeRE = regexp.MustCompile("`([^`\n]+)`")
	markdownLinkRE       = regexp.MustCompile(`\[(.+?)\]\((https?://[^)\s]+)\)`)
	markdownBoldRE       = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	markdownItalicRE     = regexp.MustCompile(`\*([^*\n]+)\*|_([^_\n]+)_`)
)

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
	for i, token := range tokens {
		escaped = strings.ReplaceAll(escaped, fmt.Sprintf("TGMARKTOKEN%dX", i), token)
	}

	return escaped
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
	)
	return replacer.Replace(text)
}
