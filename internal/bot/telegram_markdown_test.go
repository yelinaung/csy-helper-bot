package bot

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeGeneratedTelegramMarkdown(t *testing.T) {
	t.Parallel()

	input := `**Apple Inc\. \(AAPL\)** gained \+2\.05\%\!`
	want := `**Apple Inc. (AAPL)** gained +2.05%!`

	got := normalizeGeneratedTelegramMarkdown(input)
	if got != want {
		t.Fatalf("normalizeGeneratedTelegramMarkdown() = %q, want %q", got, want)
	}
}

func TestPlainTelegramMarkdownText(t *testing.T) {
	t.Parallel()

	input := `**Apple Inc\. \(AAPL\)** [Source](https://example.com/a) and _bullish_`
	want := `Apple Inc. (AAPL) Source (https://example.com/a) and bullish`

	got := plainTelegramMarkdownText(input)
	if got != want {
		t.Fatalf("plainTelegramMarkdownText() = %q, want %q", got, want)
	}
}

func TestFormatTelegramMarkdownDoesNotTreatBracketedSourceAsLinkLabel(t *testing.T) {
	t.Parallel()

	input := `_[Source: [BusinessWire](https://example.com/story)]_`

	got := formatTelegramMarkdown(input)
	if !strings.Contains(got, `[BusinessWire](https://example.com/story)`) {
		t.Fatalf("expected inner source link to be preserved, got %q", got)
	}
	if strings.Contains(got, `[_Source`) {
		t.Fatalf("expected nested bracket text not to become the link label, got %q", got)
	}
}

// TestPlainTelegramMarkdownText_StripsNULAndInvalidUTF8 verifies the
// plain-text fallback applies the same sanitization prelude as
// formatTelegramMarkdown. Before the fix, the plain path skipped
// strings.ToValidUTF8 and NUL stripping, so malformed bytes reached
// Telegram on the fallback rendering path.
func TestPlainTelegramMarkdownText_StripsNULAndInvalidUTF8(t *testing.T) {
	t.Parallel()

	// "hello\x00world\xff bad" — NUL byte and invalid UTF-8 byte 0xff.
	input := "hello\x00world\xff bad"
	got := plainTelegramMarkdownText(input)

	if strings.Contains(got, "\x00") {
		t.Fatalf("plain output contains NUL byte: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("plain output is not valid UTF-8: %q", got)
	}
}

// TestPlainAndFormatMarkdownConsistentSanitization feeds both formatters
// the same malformed input and asserts both produce NUL-free, valid UTF-8
// output. The two formatters must not drift on the safety contract.
func TestPlainAndFormatMarkdownConsistentSanitization(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"hello\x00world",
		"bad\xffbytes\xfe here",
		"\x00\x00\x00",
		"valid text \x00 with nul and \xff invalid",
	}
	for _, in := range inputs {
		plain := plainTelegramMarkdownText(in)
		formatted := formatTelegramMarkdown(in)
		if strings.Contains(plain, "\x00") {
			t.Errorf("plain output contains NUL for input %q: %q", in, plain)
		}
		if strings.Contains(formatted, "\x00") {
			t.Errorf("formatted output contains NUL for input %q: %q", in, formatted)
		}
		if !utf8.ValidString(plain) {
			t.Errorf("plain output invalid UTF-8 for input %q: %q", in, plain)
		}
		if !utf8.ValidString(formatted) {
			t.Errorf("formatted output invalid UTF-8 for input %q: %q", in, formatted)
		}
	}
}
