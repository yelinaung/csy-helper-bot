package bot

import (
	"strings"
	"testing"
	"unicode/utf8"

	"hegel.dev/go/hegel"
)

// TestPlainTelegramMarkdownText_OutputSafety is a Hegel PBT that catches
// Bug 4. The plain-text fallback must produce NUL-free, valid-UTF-8
// output for all inputs, matching the contract that
// FuzzFormatAndNormalizeMarkdown already asserts for
// formatTelegramMarkdown. Uses hegel.Binary to inject NUL and invalid
// UTF-8 bytes that hegel.Text() alone (always valid UTF-8) cannot produce.
func TestPlainTelegramMarkdownText_OutputSafety(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		// Draw arbitrary text; interleave with binary segments to get
		// NUL bytes and invalid UTF-8 sequences that full-Unicode
		// hegel.Text() never produces.
		s := hegel.Draw(ht, hegel.OneOf(
			hegel.Text().MaxSize(100),
			hegel.Map(
				hegel.Binary(0, 100),
				func(b []byte) string { return string(b) },
			),
		))
		plain := plainTelegramMarkdownText(s)
		formatted := formatTelegramMarkdown(s)

		// Property: no NUL bytes in either output.
		if strings.Contains(plain, "\x00") {
			ht.Fatalf("plain output contains NUL: %q (input=%q)", plain, s)
		}
		if strings.Contains(formatted, "\x00") {
			ht.Fatalf("formatted output contains NUL: %q (input=%q)", formatted, s)
		}

		// Property: both outputs are valid UTF-8.
		if !utf8.ValidString(plain) {
			ht.Fatalf("plain output invalid UTF-8 (input=%q)", s)
		}
		if !utf8.ValidString(formatted) {
			ht.Fatalf("formatted output invalid UTF-8 (input=%q)", s)
		}
	}, hegel.WithTestCases(200))
}

// TestFormatTelegramMarkdown_PlaceholderCollision verifies the
// TGMARKTOKEN sentinel is injection-safe. The sentinel uses a NUL-byte
// prefix; sanitizeMarkdownInput strips all NUL from untrusted input
// before addToken inserts sentinels, so the NUL prefix guarantees no
// substring of the original text can ever match the sentinel. This
// test constructs inputs with sentinel-like strings ("TGMARKTOKEN00"
// without the NUL prefix) and asserts they survive unchanged.
func TestFormatTelegramMarkdown_PlaceholderCollision(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		prefix := hegel.Draw(ht, hegel.Text().MaxSize(30))

		// A sentinel-like string WITHOUT the NUL-byte prefix. The
		// real sentinel has a NUL prefix that input can never have
		// (stripped by sanitizeMarkdownInput).
		sentinel := "TGMARKTOKEN00"

		// The literal sentinel-like string must survive — it cannot
		// collide with the real NUL-prefixed sentinel.
		a := prefix + sentinel + "**real bold**"
		out := formatTelegramMarkdown(a)
		if !strings.Contains(out, sentinel) {
			ht.Fatalf("literal sentinel was replaced: %q (input=%q)", out, a)
		}

		// Case B: different index.
		sentinel5 := "TGMARKTOKEN05"
		b := prefix + sentinel5 + "`inline code`"
		out = formatTelegramMarkdown(b)
		if !strings.Contains(out, sentinel5) {
			ht.Fatalf("literal sentinel5 was replaced: %q (input=%q)", out, b)
		}
	}, hegel.WithTestCases(100))
}
