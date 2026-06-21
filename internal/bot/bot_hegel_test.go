package bot

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

// TestParseStockCommand_Roundtrip verifies that for any symbol matching
// the regex and any valid range, the parser returns the uppercased
// symbol and correct day count.
func TestParseStockCommand_Roundtrip(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		raw := hegel.Draw(ht, hegel.FromRegex(`[A-Z0-9.\-]{1,10}`, true))
		sym := strings.ToUpper(raw)

		// Property: spot quote roundtrip.
		cmd := "!s " + sym
		gotSym, gotDays, err := parseStockCommand(cmd)
		if err != nil {
			ht.Fatalf("parseStockCommand(%q) error: %v", cmd, err)
		}
		if gotSym != sym || gotDays != 0 {
			ht.Fatalf("parseStockCommand(%q) = (%q, %d), want (%q, 0)",
				cmd, gotSym, gotDays, sym)
		}

		// Property: historical range roundtrip.
		rangeToken := hegel.Draw(ht, hegel.SampledFrom([]string{
			"7d", "30d", "60d", "90d",
		}))
		cmd = "!s " + sym + " " + rangeToken
		gotSym, gotDays, err = parseStockCommand(cmd)
		if err != nil {
			ht.Fatalf("parseStockCommand(%q) error: %v", cmd, err)
		}
		wantDays := stockRangeDays[strings.ToLower(rangeToken)]
		if gotSym != sym || gotDays != wantDays {
			ht.Fatalf("parseStockCommand(%q) = (%q, %d), want (%q, %d)",
				cmd, gotSym, gotDays, sym, wantDays)
		}
	}, hegel.WithTestCases(200))
}

// TestParseStockAnalysisCommand_Roundtrip verifies parseStockAnalysisCommand
// for any valid symbol.
func TestParseStockAnalysisCommand_Roundtrip(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		raw := hegel.Draw(ht, hegel.FromRegex(`[A-Z0-9.\-]{1,10}`, true))
		sym := strings.ToUpper(raw)

		cmd := "!sa " + sym
		gotSym, err := parseStockAnalysisCommand(cmd)
		if err != nil {
			ht.Fatalf("parseStockAnalysisCommand(%q) error: %v", cmd, err)
		}
		if gotSym != sym {
			ht.Fatalf("parseStockAnalysisCommand(%q) = %q, want %q",
				cmd, gotSym, sym)
		}
	}, hegel.WithTestCases(200))
}

// TestStockParsers_NeverPanics verifies neither parser panics on
// arbitrary text input.
func TestStockParsers_NeverPanics(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text().MaxSize(50))
		_, _, _ = parseStockCommand(text)
		_, _ = parseStockAnalysisCommand(text)
	}, hegel.WithTestCases(100))
}

// TestParseAllowedGroupIDs_Roundtrip verifies that joining a list of
// int64 IDs with commas (plus arbitrary surrounding whitespace per token)
// and parsing yields the same key set. Duplicates collapse, empty tokens
// are skipped, and the result is order-independent.
func TestParseAllowedGroupIDs_Roundtrip(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		rawIDs := hegel.Draw(ht, hegel.Lists(
			hegel.Integers[int64](math.MinInt64, math.MaxInt64),
		).MaxSize(8))

		// Build the input string with arbitrary whitespace around commas.
		parts := make([]string, 0, len(rawIDs))
		for _, id := range rawIDs {
			wsBefore := hegel.Draw(ht, hegel.Text().Alphabet(" \t"))
			wsAfter := hegel.Draw(ht, hegel.Text().Alphabet(" \t"))
			parts = append(parts, wsBefore+strconv.FormatInt(id, 10)+wsAfter)
		}
		input := strings.Join(parts, ",")

		got, err := parseAllowedGroupIDs(input)
		if err != nil {
			ht.Fatalf("parseAllowedGroupIDs(%q) error: %v", input, err)
		}

		// Property: every original ID is present (duplicates collapse).
		for _, id := range rawIDs {
			if _, ok := got[id]; !ok {
				ht.Fatalf("missing id %d in result (input=%q)", id, input)
			}
		}
		// Property: no extra IDs (skipping empty tokens and dedup).
		if len(got) > len(rawIDs) {
			ht.Fatalf("result has %d keys, input had %d ids (input=%q)",
				len(got), len(rawIDs), input)
		}
	}, hegel.WithTestCases(200))
}

// TestParseAllowedGroupIDs_NeverPanics verifies the function handles
// arbitrary text without crashing, returning either a map or an error.
func TestParseAllowedGroupIDs_NeverPanics(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text().MaxSize(60))
		result, err := parseAllowedGroupIDs(text)
		if result == nil && err == nil {
			ht.Fatalf("both result and error are nil for %q", text)
		}
	}, hegel.WithTestCases(100))
}
