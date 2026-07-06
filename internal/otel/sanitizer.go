// Package otel configures OpenTelemetry tracing, metrics, and logs export
// for the bot. It owns provider construction, credential sanitization, the
// zerolog-to-OTel log bridge, and the HTTP transport wrapper.
package otel

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// attrURLFull / attrHTTPURL are the span/log attribute keys whose values may
// carry a URL with embedded credentials. Exported as constants so the test
// suite (and the goconst linter) shares one definition.
const (
	attrURLFull = "url.full"
	attrHTTPURL = "http.url"
)

// urlAttrKeys is the set of span/log attribute keys whose values may carry a
// URL with embedded credentials. Both names are redacted to stay robust to
// semconv version drift: newer otelhttp emits url.full, older versions emit
// http.url. Redacting only one would silently leak the token if the pinned
// contrib version uses the other name — the worst failure mode for a
// fail-closed safety net.
var urlAttrKeys = map[string]struct{}{
	attrURLFull: {},
	attrHTTPURL: {},
}

// logURLAttrKeys extends urlAttrKeys with zerolog field names callers may use
// when logging a request URL (zerolog field names are caller-chosen, so cover
// the common ones).
var logURLAttrKeys = map[string]struct{}{
	"url":         {},
	attrURLFull:   {},
	attrHTTPURL:   {},
	"request.url": {},
}

// logSensitiveTextAttrKeys are zerolog fields whose values may contain
// transport errors with credential-bearing URLs embedded in the message.
var logSensitiveTextAttrKeys = map[string]struct{}{
	"error": {},
	"err":   {},
}

// spanSensitiveTextAttrKeys are span event attributes whose values may contain
// transport errors with credential-bearing URLs embedded in the message.
var spanSensitiveTextAttrKeys = map[string]struct{}{
	"exception.message": {},
}

// secretQueryValueRE matches a secret query parameter (token/api_key/apikey/
// key) and its value. The leading boundary (^, &, ?, or #) prevents
// suffixes like "key" from matching inside another key such as "monkey".
// The value run stops at the next param boundary (&, #) or end of string.
var secretQueryValueRE = regexp.MustCompile(`(?i)(^|[&?#])(token|api_key|apikey|key)=([^&#]*)`)

// telegramBotTokenPathRE matches the bot<TOKEN> path segment produced by the
// Telegram file download API (e.g. https://api.telegram.org/file/bot123:abc/...).
var telegramBotTokenPathRE = regexp.MustCompile(`bot\d+:[A-Za-z0-9_-]+`)

// sensitiveURLRE matches URL substrings inside larger messages such as
// net/url.Error strings: `Get "https://...token=secret": dial tcp ...`.
var sensitiveURLRE = regexp.MustCompile(`(?i)https?://[^\s"'<>()]+`)

const (
	redactedPlaceholder             = "<redacted>"
	sensitiveURLTrailingPunctuation = ",;:."
)

// redactURL strips credentials from a URL string. It redacts secret query
// parameters (token/api_key/apikey/key) and Telegram bot-token path segments
// (bot<TOKEN>). It operates on the raw string so the "<redacted>" placeholder
// is preserved literally (a url.Values round-trip would percent-encode the
// angle brackets). On any parse failure it returns "<redacted>" (fail-closed)
// so a malformed URL can never leak a credential. Non-credential URLs pass
// through unchanged.
func redactURL(raw string) string {
	if _, err := url.Parse(raw); err != nil {
		return redactedPlaceholder
	}

	result := raw

	// Redact secret query parameter values. ReplaceAllStringFunc preserves the
	// leading boundary character and key, swapping only the value.
	if secretQueryValueRE.MatchString(result) {
		result = secretQueryValueRE.ReplaceAllStringFunc(result, func(match string) string {
			eq := strings.IndexByte(match, '=')
			if eq < 0 {
				return match
			}
			key := strings.ToLower(match[:eq])
			// Strip the leading boundary char to isolate the key name.
			key = strings.TrimLeft(key, "&?#")
			if _, ok := secretQueryParamKeys[key]; !ok {
				return match
			}
			return match[:eq+1] + redactedPlaceholder
		})
	}

	// Redact Telegram bot-token path segments.
	if telegramBotTokenPathRE.MatchString(result) {
		result = telegramBotTokenPathRE.ReplaceAllString(result, "bot"+redactedPlaceholder)
	}

	return result
}

// RedactSensitiveText strips URL-embedded credentials from arbitrary text.
func RedactSensitiveText(text string) string {
	if text == "" {
		return text
	}
	return sensitiveURLRE.ReplaceAllStringFunc(text, func(match string) string {
		trimmed := strings.TrimRight(match, sensitiveURLTrailingPunctuation)
		return redactURL(trimmed) + match[len(trimmed):]
	})
}

// SanitizeError returns an error whose Error string has URL-embedded
// credentials redacted. The returned error unwraps to the original error so
// callers can still use errors.Is and errors.As.
func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	msg := RedactSensitiveText(err.Error())
	if msg == err.Error() {
		return err
	}
	return sanitizedError{err: err, msg: msg}
}

// SanitizeErrorValue is suitable for zerolog.ErrorMarshalFunc.
func SanitizeErrorValue(err error) any {
	if err == nil {
		return nil
	}
	return SanitizeError(err).Error()
}

type sanitizedError struct {
	err error
	msg string
}

func (e sanitizedError) Error() string {
	return e.msg
}

func (e sanitizedError) Unwrap() error {
	return e.err
}

func (e sanitizedError) Is(target error) bool {
	return errors.Is(e.err, target)
}

func (e sanitizedError) As(target any) bool {
	return errors.As(e.err, target)
}

// secretQueryParamKeys are query parameter names (lowercase) whose values are
// redacted.
var secretQueryParamKeys = map[string]struct{}{
	"token":   {},
	"api_key": {},
	"apikey":  {},
	"key":     {},
}
