package bot

import (
	"strings"
	"testing"
	"unicode/utf8"

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

// runeByteOffsets returns a slice of byte offsets for each rune boundary
// in s. offsets[i] is the byte offset of the i-th rune; offsets[len(offsets)-1] == len(s).
func runeByteOffsets(s string) []int {
	offsets := []int{0}
	for i := range s {
		if i == offsets[len(offsets)-1] {
			_, size := utf8.DecodeRuneInString(s[i:])
			offsets = append(offsets, i+size)
		}
	}
	return offsets
}

// utf16Len returns the UTF-16 code unit count of s.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16UnitsForRune(r)
	}
	return n
}

// TestUTF16EntityRangeToByteRange_Roundtrip is a correctness property
// stronger than the existing FuzzUTF16EntityRangeToByteRange. The fuzz
// test only checks the returned range is valid (0 <= start <= end <=
// len(text)), not that it is *correct*. This property picks a random
// rune-bounded substring of text, computes its UTF-16 offset and length,
// calls utf16EntityRangeToByteRange, and asserts text[start:end] == sub.
//
// Rune boundaries are used (not raw byte offsets) because UTF-16 entity
// ranges from Telegram always correspond to rune boundaries — an entity
// range can never start or end inside a multi-byte UTF-8 rune.
func TestUTF16EntityRangeToByteRange_Roundtrip(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text().MaxSize(60))
		offsets := runeByteOffsets(text)
		// offsets has len = runeCount+1, with offsets[0]=0 and
		// offsets[runeCount]=len(text).
		runeCount := len(offsets) - 1

		// Draw rune indices; allow empty substring (startRune == endRune).
		startRune := hegel.Draw(ht, hegel.Integers(0, runeCount))
		endRune := hegel.Draw(ht, hegel.Integers(startRune, runeCount))

		startByte := offsets[startRune]
		endByte := offsets[endRune]
		sub := text[startByte:endByte]

		// Compute UTF-16 offset: sum utf16UnitsForRune over text[:startByte].
		prefix := text[:startByte]
		utf16Offset := utf16Len(prefix)

		// Compute UTF-16 length of the substring.
		utf16Length := utf16Len(sub)

		// The function rejects length <= 0 by contract (a 0-length
		// entity range makes no sense for Telegram). Skip those cases.
		if utf16Length <= 0 {
			ht.Assume(false)
		}

		gotStart, gotEnd, ok := utf16EntityRangeToByteRange(text, utf16Offset, utf16Length)
		if !ok {
			ht.Fatalf("unexpected !ok for text=%q offset=%d length=%d sub=%q",
				text, utf16Offset, utf16Length, sub)
		}
		gotSub := text[gotStart:gotEnd]
		if gotSub != sub {
			ht.Fatalf("text[%d:%d] = %q, want %q (offset=%d length=%d)",
				gotStart, gotEnd, gotSub, sub, utf16Offset, utf16Length)
		}
	}, hegel.WithTestCases(200))
}

// TestUTF16EntityRangeToByteRange_RejectsInvalid verifies the function
// rejects inputs outside its contract: negative offset, zero/negative
// length, and ranges that extend beyond the text.
func TestUTF16EntityRangeToByteRange_RejectsInvalid(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text().MaxSize(30))

		// Case 1: negative offset -> false.
		_, _, ok := utf16EntityRangeToByteRange(text, -1, 5)
		if ok {
			ht.Fatalf("expected false for offset=-1")
		}

		// Case 2: non-positive length -> false.
		_, _, ok = utf16EntityRangeToByteRange(text, 0, 0)
		if ok {
			ht.Fatalf("expected false for length=0")
		}
		_, _, ok = utf16EntityRangeToByteRange(text, 0, -1)
		if ok {
			ht.Fatalf("expected false for length=-1")
		}

		// Case 3: range extends beyond text -> false. Compute the UTF-16
		// length of the text, then request offset=0, length=textLen+1.
		textUTF16Len := utf16Len(text)
		_, _, ok = utf16EntityRangeToByteRange(text, 0, textUTF16Len+10)
		if ok {
			ht.Fatalf("expected false for offset=0 length=%d (text UTF-16 len=%d)",
				textUTF16Len+10, textUTF16Len)
		}
	}, hegel.WithTestCases(50))
}

// TestUTF16EntityRangeToByteRange_NeverPanics verifies the function does
// not crash on arbitrary text and arbitrary offset/length combinations.
func TestUTF16EntityRangeToByteRange_NeverPanics(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.OneOf(
			hegel.Text().MaxSize(40),
			hegel.Map(
				hegel.Binary(0, 40),
				func(b []byte) string { return string(b) },
			),
		))
		offset := hegel.Draw(ht, hegel.Integers(-50, 500))
		length := hegel.Draw(ht, hegel.Integers(-10, 200))

		start, end, ok := utf16EntityRangeToByteRange(text, offset, length)
		if ok && (start < 0 || end < start || end > len(text)) {
			ht.Fatalf("invalid range returned: start=%d end=%d len=%d",
				start, end, len(text))
		}
	}, hegel.WithTestCases(100))
}
