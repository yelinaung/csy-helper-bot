package bot

import (
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

// TestMentionAndSuffixFromText_FindsMentionWithUnicodeBoundaries is a
// soundness PBT that catches Bug 2. The function promises to find
// targetMention with non-username-char boundaries; before the fix it
// silently failed for Unicode prefixes whose lowercase form has a
// different UTF-8 byte length (ẞ U+1E9E → ß, İ U+0130 → i̇), because
// strings.ToLower shifted every subsequent byte offset.
//
// Property: for text = prefix + mention + suffix where prefix and suffix
// are arbitrary full-Unicode strings, prefix does not end with an
// isTelegramUsernameChar, and suffix does not start with one,
// mentionAndSuffixFromText(text, mention) returns (mention, suffix, true).
//
// Uses hegel.Text() (full Unicode) so İ, ẞ, combining marks, and emoji
// all appear in prefix/suffix and exercise the byte-shift edge cases.
func TestMentionAndSuffixFromText_FindsMentionWithUnicodeBoundaries(t *testing.T) {
	const mention = "@csy_helper_dev_bot"

	hegel.Test(t, func(ht *hegel.T) {
		prefix := hegel.Draw(ht, hegel.Text().MaxSize(20))
		suffix := hegel.Draw(ht, hegel.Text().MaxSize(20))

		// Guard: prefix must not end with a username char, otherwise
		// hasMentionBoundaries correctly rejects the match (the char
		// before @ would look like part of the username). Only check
		// the last byte since isTelegramUsernameChar is byte-based.
		if len(prefix) > 0 && isTelegramUsernameChar(prefix[len(prefix)-1]) {
			ht.Assume(false)
		}
		// Guard: suffix must not start with a username char, otherwise
		// hasMentionBoundaries correctly rejects the match.
		if len(suffix) > 0 && isTelegramUsernameChar(suffix[0]) {
			ht.Assume(false)
		}

		text := prefix + mention + suffix
		gotMention, gotSuffix, ok := mentionAndSuffixFromText(text, mention)

		if !ok {
			ht.Fatalf("expected ok=true for prefix=%q suffix=%q text=%q",
				prefix, suffix, text)
		}
		if gotMention != mention {
			ht.Fatalf("mention = %q, want %q (prefix=%q suffix=%q)",
				gotMention, mention, prefix, suffix)
		}
		if gotSuffix != suffix {
			ht.Fatalf("suffix = %q, want %q (prefix=%q suffix=%q)",
				gotSuffix, suffix, prefix, suffix)
		}
	}, hegel.WithTestCases(200))
}

// TestMentionAndSuffixFromText_CaseInsensitiveMatch verifies the function
// matches the mention regardless of case in the surrounding text, since
// Telegram usernames are case-insensitive. The mention itself may appear
// in any case in the text and should be returned exactly as it appears.
func TestMentionAndSuffixFromText_CaseInsensitiveMatch(t *testing.T) {
	const mention = "@csy_helper_dev_bot"

	hegel.Test(t, func(ht *hegel.T) {
		// Generate a case variant of the mention by randomly uppercasing
		// some runes. The function should still find it.
		upperMention := strings.ToUpper(mention)
		// Use OneOf to pick either the original or fully-upper variant.
		textMention := hegel.Draw(ht, hegel.OneOf(
			hegel.Just(mention),
			hegel.Just(upperMention),
		))
		prefix := hegel.Draw(ht, hegel.Text().MaxSize(10))
		suffix := hegel.Draw(ht, hegel.Text().MaxSize(10))

		if len(prefix) > 0 && isTelegramUsernameChar(prefix[len(prefix)-1]) {
			ht.Assume(false)
		}
		if len(suffix) > 0 && isTelegramUsernameChar(suffix[0]) {
			ht.Assume(false)
		}

		text := prefix + textMention + suffix
		gotMention, gotSuffix, ok := mentionAndSuffixFromText(text, mention)

		if !ok {
			ht.Fatalf("expected ok=true for case-insensitive match "+
				"(prefix=%q mention=%q suffix=%q)",
				prefix, textMention, suffix)
		}
		// The returned mention should match what appeared in the text,
		// not the canonical lowercase form.
		if gotMention != textMention {
			ht.Fatalf("mention = %q, want %q (the form in text)",
				gotMention, textMention)
		}
		if gotSuffix != suffix {
			ht.Fatalf("suffix = %q, want %q", gotSuffix, suffix)
		}
	}, hegel.WithTestCases(100))
}

// TestMentionAndSuffixFromText_NeverPanics is a robustness property: the
// function should handle arbitrary text (including invalid UTF-8, empty
// strings, NUL bytes) without panicking, returning (anything, anything,
// false) when the mention is not found.
func TestMentionAndSuffixFromText_NeverPanics(t *testing.T) {
	const mention = "@csy_helper_dev_bot"

	hegel.Test(t, func(ht *hegel.T) {
		// Draw arbitrary bytes as text — hegel.Text() produces valid
		// UTF-8, so also draw from Binary to get invalid sequences.
		text := hegel.Draw(ht, hegel.OneOf(
			hegel.Text().MaxSize(50),
			hegel.Map(
				hegel.Binary(0, 50),
				func(b []byte) string { return string(b) },
			),
		))

		// Should never panic.
		mentionResult, suffixResult, okResult := mentionAndSuffixFromText(text, mention)
		_ = mentionResult
		_ = suffixResult
		_ = okResult
	}, hegel.WithTestCases(100))
}
