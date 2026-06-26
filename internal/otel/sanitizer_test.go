package otel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactURL_FinnhubToken(t *testing.T) {
	t.Parallel()

	in := "https://finnhub.io/api/v1/quote?symbol=AAPL&token=secret-key"
	got := redactURL(in)

	require.Contains(t, got, "token=<redacted>")
	require.NotContains(t, got, "secret-key")
	require.Contains(t, got, "symbol=AAPL")
}

func TestRedactURL_TelegramBotToken(t *testing.T) {
	t.Parallel()

	in := "https://api.telegram.org/file/bot123456:ABC-DEF_ghi/file.jpg"
	got := redactURL(in)

	require.Contains(t, got, "bot<redacted>")
	require.NotContains(t, got, "123456:ABC-DEF_ghi")
}

func TestRedactURL_OtherSecretQueryKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key   string
		value string
	}{
		{"api_key", "abc"},
		{"apikey", "abc"},
		{"key", "abc"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			in := "https://example.com/path?" + tc.key + "=" + tc.value + "&keep=1"
			got := redactURL(in)
			require.Contains(t, got, "<redacted>")
			require.NotContains(t, got, tc.value)
			require.Contains(t, got, "keep=1")
		})
	}
}

func TestRedactURL_NoSecretUnchanged(t *testing.T) {
	t.Parallel()

	in := "https://example.com/api/v1/quote?symbol=AAPL&limit=4"
	got := redactURL(in)
	require.Equal(t, in, got)
}

func TestRedactURL_FailClosedOnGarbage(t *testing.T) {
	t.Parallel()

	// url.Parse is extremely permissive; a control byte in the host still
	// parses. Feed it something the parser rejects.
	in := "ht\x00tp://\x7f"
	got := redactURL(in)
	require.Equal(t, redactedPlaceholder, got)
}

func TestRedactURL_Empty(t *testing.T) {
	t.Parallel()

	// Empty string parses fine and has no credentials — returns unchanged.
	require.Empty(t, redactURL(""))
}
