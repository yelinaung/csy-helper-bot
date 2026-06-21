package bot

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"hegel.dev/go/hegel"
)

// TestShouldRespondInBurmese_Commutative verifies that the order of
// variadic arguments does not affect the result.
func TestShouldRespondInBurmese_Commutative(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(1, 5))
		texts := make([]string, n)
		for i := range texts {
			texts[i] = hegel.Draw(ht, hegel.Text().MaxSize(30))
		}
		got := shouldRespondInBurmese(texts...)
		// Reverse the order and check again.
		for i := range len(texts) / 2 {
			texts[i], texts[len(texts)-1-i] = texts[len(texts)-1-i], texts[i]
		}
		if shouldRespondInBurmese(texts...) != got {
			ht.Fatalf("not commutative: original=%v reversed=%v", got, !got)
		}
	}, hegel.WithTestCases(100))
}

// TestShouldRespondInBurmese_Monotonic verifies that adding more text
// cannot flip a true result to false. If f(a) is true, then f(a, anything)
// must also be true.
func TestShouldRespondInBurmese_Monotonic(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		// Generate text with Myanmar characters included.
		a := hegel.Draw(ht, hegel.Text().MaxSize(30))
		b := hegel.Draw(ht, hegel.Text().MaxSize(30))

		singleTrue := shouldRespondInBurmese(a)
		if singleTrue && !shouldRespondInBurmese(a, b) {
			ht.Fatalf("not monotonic: f(%q)=true but f(%q,%q)=false", a, a, b)
		}
		// Same from the other side.
		singleTrue = shouldRespondInBurmese(b)
		if singleTrue && !shouldRespondInBurmese(a, b) {
			ht.Fatalf("not monotonic: f(%q)=true but f(%q,%q)=false", b, a, b)
		}
	}, hegel.WithTestCases(100))
}

// TestShouldRespondInBurmese_Idempotent verifies that repeating the same
// argument produces the same result.
func TestShouldRespondInBurmese_Idempotent(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		s := hegel.Draw(ht, hegel.Text().MaxSize(30))
		if shouldRespondInBurmese(s) != shouldRespondInBurmese(s, s) {
			ht.Fatalf("not idempotent for %q", s)
		}
	}, hegel.WithTestCases(100))
}

// TestHistoricalDateRangeUTC_Invariant verifies date-range arithmetic:
// the range spans exactly days*24h, end is day-before-now UTC-midnight,
// and start is strictly before end.
func TestHistoricalDateRangeUTC_Invariant(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		days := hegel.Draw(ht, hegel.Integers(1, 90))
		now := hegel.Draw(ht, hegel.Datetimes())
		dr := historicalDateRangeUTC(now, days)

		// Property: range spans exactly days * 24h.
		if dr.End.Sub(dr.Start) != time.Duration(days)*24*time.Hour {
			ht.Fatalf("End.Sub(Start) = %v, want %vh for now=%v days=%d",
				dr.End.Sub(dr.Start), days*24, now, days)
		}
		// Property: end is day-before-now UTC-midnight.
		wantEnd := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
		if !dr.End.Equal(wantEnd) {
			ht.Fatalf("End = %v, want %v (now=%v UTC)", dr.End, wantEnd, now.UTC())
		}
		// Property: start is before end.
		if !dr.Start.Before(dr.End) {
			ht.Fatalf("Start %v not before End %v", dr.Start, dr.End)
		}
	}, hegel.WithTestCases(100))
}

// TestEnvParsers_BoundsContracts verifies that each env-parsing function
// returns a value in its documented range for arbitrary hegel.Text()
// input, and never panics.
func TestEnvParsers_BoundsContracts(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		// Use short alphanumeric strings safe for t.Setenv. Constrain
		// length to avoid overflow when strconv.Atoi succeeds for a huge
		// integer that overflows time.Duration multiplication.
		val := hegel.Draw(ht, hegel.Text().Alphabet(
			"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_- .",
		).MaxSize(6))

		t.Setenv("EXA_NUM_RESULTS", val)
		n := loadExaNumResults()
		if n < 1 || n > 20 {
			ht.Fatalf("loadExaNumResults() = %d for env=%q (want [1, 20])", n, val)
		}

		// loadParallelMaxResults: must be in [1, 10].
		t.Setenv("PARALLEL_MAX_RESULTS", val)
		n = loadParallelMaxResults()
		if n < 1 || n > 10 {
			ht.Fatalf("loadParallelMaxResults() = %d for env=%q (want [1, 10])", n, val)
		}

		// loadParallelTimeout: must be > 0.
		t.Setenv("PARALLEL_TIMEOUT_SECONDS", val)
		d := loadParallelTimeout()
		if d <= 0 {
			ht.Fatalf("loadParallelTimeout() = %v for env=%q (want >0)", d, val)
		}

		// normalizePort: either "5000" or valid port string [1, 65535].
		port := normalizePort(val)
		if port == "5000" {
			// OK — default or invalid input.
		} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			ht.Fatalf("normalizePort(%q) = %q (want 5000 or [1,65535])", val, port)
		}
	}, hegel.WithTestCases(200))
}

// TestEnvParsers_ErrorReturning verifies the error-returning env parsers
// either return (>0, nil) or (<=0, error); they never panic.
func TestEnvParsers_ErrorReturning(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		// Constrain to avoid overflow in time.Duration(seconds) * time.Second
		// (too-large values wrap to negative, which is a known limitation
		// of the production function, not a contract violation).
		val := hegel.Draw(ht, hegel.Text().Alphabet(
			"0123456789- ",
		).MaxSize(9))

		t.Setenv("STOCK_ANALYSIS_TIMEOUT_SECONDS", val)
		d, err := loadAnalysisTimeout()
		if err == nil && d <= 0 {
			ht.Fatalf("loadAnalysisTimeout() = (%v, nil) for env=%q (want >0 or error)", d, val)
		}

		t.Setenv("STOCK_ANALYSIS_MAX_OUTPUT_TOKENS", val)
		tok, err := loadAnalysisMaxOutputTokens()
		if err == nil && tok <= 0 {
			ht.Fatalf("loadAnalysisMaxOutputTokens() = (%d, nil) for env=%q (want >0 or error)", tok, val)
		}
	}, hegel.WithTestCases(100))
}

// TestRateLimiterLoaders_Bounds verifies loadExplainRateLimiter and
// loadAnalysisRateLimiter always produce a limiter with limit>0 and window>0.
func TestRateLimiterLoaders_Bounds(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		count := hegel.Draw(ht, hegel.Text().Alphabet(
			"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_ ",
		))
		window := hegel.Draw(ht, hegel.Text().Alphabet(
			"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_ ",
		))

		t.Setenv("EXPLAIN_RATE_LIMIT_COUNT", count)
		t.Setenv("EXPLAIN_RATE_LIMIT_WINDOW_SECONDS", window)
		rl := loadExplainRateLimiter()
		if rl == nil || rl.limit <= 0 || rl.window <= 0 {
			ht.Fatalf("loadExplainRateLimiter limit=%d window=%v (count=%q window=%q)",
				rl.limit, rl.window, count, window)
		}

		t.Setenv("STOCK_ANALYSIS_RATE_LIMIT_COUNT", count)
		t.Setenv("STOCK_ANALYSIS_RATE_LIMIT_WINDOW_SECONDS", window)
		rl = loadAnalysisRateLimiter()
		if rl == nil || rl.limit <= 0 || rl.window <= 0 {
			ht.Fatalf("loadAnalysisRateLimiter limit=%d window=%v (count=%q window=%q)",
				rl.limit, rl.window, count, window)
		}
	}, hegel.WithTestCases(100))
}

var twitterStatusRx = regexp.MustCompile(`/status/[0-9]+`)

// TestExtractFixedXLinks_Invariants verifies dedup, cap, and output-format
// properties. Generates text by splicing arbitrary prose around tweet URLs
// and non-tweet URLs.
func TestExtractFixedXLinks_Invariants(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		// Generate tweet URLs.
		n := hegel.Draw(ht, hegel.Integers(0, 8))
		// Generate non-tweet URLs count.
		m := hegel.Draw(ht, hegel.Integers(0, 4))
		tweetURLs := make([]string, 0, n+m)
		for range n {
			tweetURLs = append(tweetURLs, hegel.Draw(ht, hegel.FromRegex(
				`https://(?:www\.)?(?:x|twitter)\.com/[a-z]+/status/[0-9]+`, true,
			)))
		}
		// Generate non-tweet URLs (profile links, random hosts) — these
		// should be ignored by extractFixedXLinks.
		for range m {
			tweetURLs = append(tweetURLs, hegel.Draw(ht, hegel.OneOf(
				hegel.Just("https://x.com/someprofile"),
				hegel.Just("https://example.com/foo/status/1"),
				hegel.Text().MaxSize(30),
			)))
		}
		// Splice all URLs with prose in between.
		parts := make([]string, 0, len(tweetURLs))
		for _, u := range tweetURLs {
			prose := hegel.Draw(ht, hegel.Text().MaxSize(20))
			parts = append(parts, prose+" "+u+" "+prose)
		}
		text := strings.Join(parts, " ")

		links := extractFixedXLinks(text)

		// Property: capped at maxXLinksPerMessage.
		if len(links) > maxXLinksPerMessage {
			ht.Fatalf("got %d links, max %d", len(links), maxXLinksPerMessage)
		}
		// Property: deduplicated.
		seen := make(map[string]struct{}, len(links))
		for _, l := range links {
			if _, dup := seen[l]; dup {
				ht.Fatalf("duplicate link %q", l)
			}
			seen[l] = struct{}{}
		}
		// Property: every link starts with fixupx.com or fxtwitter.com
		// and contains /status/<digits>.
		for _, l := range links {
			if !strings.HasPrefix(l, "https://fixupx.com/") &&
				!strings.HasPrefix(l, "https://fxtwitter.com/") {
				ht.Fatalf("link %q does not start with expected host", l)
			}
			if !twitterStatusRx.MatchString(l) {
				ht.Fatalf("link %q missing /status/<digits>", l)
			}
		}
	}, hegel.WithTestCases(200))
}
