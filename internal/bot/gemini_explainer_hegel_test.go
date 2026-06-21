package bot

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"hegel.dev/go/hegel"
)

// TestSanitizeForPrompt_Idempotent verifies that applying sanitizeForPrompt
// twice produces the same result as applying it once. As a normalization
// function (strip NUL, replace invalid UTF-8, truncate runes), it must be
// idempotent — the first pass already removes everything the second pass
// would check for.
func TestSanitizeForPrompt_Idempotent(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		s := hegel.Draw(ht, hegel.Text().MaxSize(100))
		n := hegel.Draw(ht, hegel.Integers(0, 200))
		once := sanitizeForPrompt(s, n)
		twice := sanitizeForPrompt(once, n)
		if once != twice {
			ht.Fatalf("not idempotent: once=%q twice=%q (input=%q n=%d)",
				once, twice, s, n)
		}
	}, hegel.WithTestCases(200))
}

// TestSanitizeForPrompt_NeverPanics verifies the function handles
// arbitrary inputs without crashing.
func TestSanitizeForPrompt_NeverPanics(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		s := hegel.Draw(ht, hegel.Text().MaxSize(80))
		n := hegel.Draw(ht, hegel.Integers(-5, 200))
		out := sanitizeForPrompt(s, n)
		// No panic is the assertion.
		_ = out
	}, hegel.WithTestCases(100))
}

// TestSanitizeForPrompt_OutputSafety asserts the output is always valid
// UTF-8, NUL-free, and does not exceed the rune budget.
func TestSanitizeForPrompt_OutputSafety(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		s := hegel.Draw(ht, hegel.Text().MaxSize(100))
		n := hegel.Draw(ht, hegel.Integers(0, 200))
		out := sanitizeForPrompt(s, n)
		if strings.Contains(out, "\x00") {
			ht.Fatalf("output contains NUL byte: %q (input=%q)", out, s)
		}
		if !utf8.ValidString(out) {
			ht.Fatalf("output is invalid UTF-8: %q (input=%q)", out, s)
		}
		if n > 0 && utf8.RuneCountInString(out) > n {
			ht.Fatalf("output rune count %d > budget %d",
				utf8.RuneCountInString(out), n)
		}
	}, hegel.WithTestCases(200))
}

// TestSanitizeExaResults_Idempotent verifies that applying the Exa result
// sanitizer twice produces the same output as applying it once.
func TestSanitizeExaResults_Idempotent(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(0, 10))
		results := make([]exaSearchResult, n)
		for i := range results {
			results[i] = exaSearchResult{
				Title:      hegel.Draw(ht, hegel.Text().MaxSize(200)),
				URL:        hegel.Draw(ht, hegel.Text().MaxSize(100)),
				Author:     hegel.Draw(ht, hegel.Text().MaxSize(150)),
				Highlights: hegel.Draw(ht, hegel.Lists(hegel.Text().MaxSize(300)).MaxSize(5)),
			}
		}
		once := sanitizeExaResults(results)
		twice := sanitizeExaResults(once)
		if len(once) != len(twice) {
			ht.Fatalf("len(once)=%d len(twice)=%d", len(once), len(twice))
		}
		for i := range once {
			if !slices.Equal(once[i].Highlights, twice[i].Highlights) ||
				once[i].Title != twice[i].Title ||
				once[i].URL != twice[i].URL ||
				once[i].Author != twice[i].Author {
				ht.Fatalf("not idempotent at %d: once=%+v twice=%+v",
					i, once[i], twice[i])
			}
		}
	}, hegel.WithTestCases(200))
}

// TestSanitizeParallelResults_Idempotent is the same idempotence property
// for the Parallel Search result sanitizer.
func TestSanitizeParallelResults_Idempotent(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(0, 10))
		results := make([]parallelSearchResult, n)
		for i := range results {
			results[i] = parallelSearchResult{
				Title:       hegel.Draw(ht, hegel.Text().MaxSize(200)),
				URL:         hegel.Draw(ht, hegel.Text().MaxSize(100)),
				PublishDate: hegel.Draw(ht, hegel.Text().MaxSize(50)),
				Excerpts:    hegel.Draw(ht, hegel.Lists(hegel.Text().MaxSize(400)).MaxSize(5)),
			}
		}
		once := sanitizeParallelResults(results)
		twice := sanitizeParallelResults(once)
		if len(once) != len(twice) {
			ht.Fatalf("len(once)=%d len(twice)=%d", len(once), len(twice))
		}
		for i := range once {
			a, b := once[i], twice[i]
			if !slices.Equal(a.Excerpts, b.Excerpts) ||
				a.Title != b.Title || a.URL != b.URL {
				ht.Fatalf("not idempotent at %d: once=%+v twice=%+v", i, a, b)
			}
		}
	}, hegel.WithTestCases(200))
}
