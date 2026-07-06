package otel

import (
	"errors"
	"fmt"
	"net/url"
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

func TestRedactURL_FragmentSecretQueryKey(t *testing.T) {
	t.Parallel()

	in := "https://example.com/path?symbol=AAPL#token=fragment-secret"
	got := redactURL(in)

	require.Contains(t, got, "#token=<redacted>")
	require.NotContains(t, got, "fragment-secret")
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

func TestRedactSensitiveText_RedactsURLsInsideErrorMessages(t *testing.T) {
	t.Parallel()

	input := `Get "https://finnhub.io/api/v1/quote?symbol=AAPL&token=finnhub-secret": dial tcp: lookup failed; Get "https://api.telegram.org/file/bot123456:ABC-DEF_ghi/photos/file.jpg": EOF`
	got := RedactSensitiveText(input)

	require.Contains(t, got, "token=<redacted>")
	require.Contains(t, got, "bot<redacted>")
	require.NotContains(t, got, "finnhub-secret")
	require.NotContains(t, got, "123456:ABC-DEF_ghi")
}

func TestRedactSensitiveText_RedactsUppercaseScheme(t *testing.T) {
	t.Parallel()

	input := `Get "HTTPS://finnhub.io/api/v1/quote?symbol=AAPL&token=upper-secret": dial tcp`
	got := RedactSensitiveText(input)

	require.Contains(t, got, "token=<redacted>")
	require.NotContains(t, got, "upper-secret")
}

func TestRedactSensitiveText_PreservesTrailingPunctuation(t *testing.T) {
	t.Parallel()

	input := "failed at https://finnhub.io/api/v1/quote?token=secret, retrying."
	got := RedactSensitiveText(input)

	require.Equal(t, "failed at https://finnhub.io/api/v1/quote?token=<redacted>, retrying.", got)
}

func TestSanitizeError_Nil(t *testing.T) {
	t.Parallel()

	require.NoError(t, SanitizeError(nil))
}

func TestSanitizeError_NoSensitiveURL(t *testing.T) {
	t.Parallel()

	err := errors.New("oops")
	got := SanitizeError(err)

	require.Same(t, err, got)
	require.Equal(t, "oops", got.Error())
}

func TestSanitizeErrorValue_Nil(t *testing.T) {
	t.Parallel()

	require.Nil(t, SanitizeErrorValue(nil))
}

func TestSanitizeErrorValue_NoSensitiveURL(t *testing.T) {
	t.Parallel()

	err := errors.New("oops")
	got := SanitizeErrorValue(err)

	require.IsType(t, "", got)
	require.Equal(t, err.Error(), got)
}

func TestSanitizeError_RedactsAndPreservesUnwrap(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("network failed")
	urlErr := &url.Error{
		Op:  "Get",
		URL: "https://finnhub.io/api/v1/quote?symbol=AAPL&token=finnhub-secret",
		Err: sentinel,
	}
	err := fmt.Errorf("fetch stock quote: %w", urlErr)

	safeErr := SanitizeError(err)

	require.ErrorIs(t, safeErr, sentinel)
	require.NotContains(t, safeErr.Error(), "finnhub-secret")
	require.Contains(t, safeErr.Error(), "token=<redacted>")
}
