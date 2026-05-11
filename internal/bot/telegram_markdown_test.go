package bot

import (
	"strings"
	"testing"
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
