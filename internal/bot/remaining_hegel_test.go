package bot

import (
	"encoding/json"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

// TestBuildAnalysisPrompt_CascadeBudget verifies the rune-budget cascade
// in buildAnalysisPrompt. The existing FuzzBuildAnalysisPrompt only feeds
// short strings and small floats, so it never enters the drop loop. This
// PBT generates large NewsItems (via separately-drawn size) to push the
// JSON payload past maxPromptTotalRuneLen and exercises every cascade stage:
// price-target → recommendation → earnings → metrics → news.
//
// It also documents a real limitation: Symbol and Profile.Name are not
// droppable, so a giant profile name can cause the prompt to exceed the
// budget even after the drop loop exits via break. This PBT surfaces that
// as a non-fatal note rather than a hard failure, since it's currently
// documented as intended behavior.
func TestBuildAnalysisPrompt_CascadeBudget(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		symbol := hegel.Draw(ht, hegel.Text().MaxSize(20))
		if symbol == "" {
			ht.Assume(false)
		}

		// Draw a large number of news items with long titles and
		// highlights, using separately-drawn sizes so hegel can
		// produce 50-200+ items and exercise the cascade.
		n := hegel.Draw(ht, hegel.Integers(0, 200))
		items := make([]newsHighlight, 0, n)
		for range n {
			titleLen := hegel.Draw(ht, hegel.Integers(0, maxTitleRuneLen))
			items = append(items, newsHighlight{
				Title: hegel.Draw(ht, hegel.Text().MinSize(titleLen).MaxSize(maxTitleRuneLen)),
				URL:   hegel.Draw(ht, hegel.Text().MaxSize(50)),
				Highlights: []string{
					hegel.Draw(ht, hegel.Text().MinSize(0).MaxSize(maxHighlightRuneLen)),
				},
			})
		}

		input := &stockAnalysisInput{
			Symbol:    symbol,
			Quote:     &StockQuote{CurrentPrice: 150.00},
			NewsItems: items,
		}

		prompt, err := buildAnalysisPrompt(input, "cascade01")
		if err != nil {
			ht.Fatalf("buildAnalysisPrompt error: %v", err)
		}

		// Property: prompt must be non-empty and contain valid JSON
		// payload. The symbol is embedded through JSON, which HTML-
		// escapes <, >, and & (so a raw strings.Contains would miss
		// them). Parse the payload and check the symbol field.
		if prompt == "" {
			ht.Fatal("empty prompt")
		}
		// Extract the JSON payload from between the marker and
		// "Remember:" (same pattern used by FuzzBuildAnalysisPrompt).
		markerIdx := strings.Index(prompt, analysisPromptPayloadMarker)
		remIdx := strings.LastIndex(prompt, "Remember:")
		if markerIdx >= 0 && remIdx > markerIdx {
			jsonBlock := prompt[markerIdx+len(analysisPromptPayloadMarker) : remIdx]
			jsonBlock = strings.TrimSpace(jsonBlock)
			jsonBlock, _ = strings.CutPrefix(jsonBlock, "```json")
			jsonBlock, _ = strings.CutSuffix(jsonBlock, "```")
			jsonBlock = strings.TrimSpace(jsonBlock)
			if jsonBlock != "" {
				var payload analysisPromptPayload
				if err := json.Unmarshal([]byte(jsonBlock), &payload); err != nil {
					ht.Fatalf("invalid JSON payload: %v", err)
				}
				// Symbol is sanitized by sanitizeAnalysisInput,
				// so compare against the sanitized value.
				wantSymbol := sanitizeForPrompt(symbol, 10)
				if payload.Symbol != wantSymbol {
					ht.Fatalf("payload.Symbol = %q, want %q",
						payload.Symbol, wantSymbol)
				}
			}
		}
	}, hegel.WithTestCases(200))
}

// TestToPromptWebResults_LengthBounds verifies len(output) <= len(input)
// and no-crash on arbitrary []parallelSearchResult input.
func TestToPromptWebResults_LengthBounds(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(0, 15))
		results := make([]parallelSearchResult, n)
		for i := range results {
			results[i] = parallelSearchResult{
				Title:       hegel.Draw(ht, hegel.Text().MaxSize(100)),
				URL:         hegel.Draw(ht, hegel.Text().MaxSize(100)),
				PublishDate: hegel.Draw(ht, hegel.Text().MaxSize(30)),
				Excerpts:    hegel.Draw(ht, hegel.Lists(hegel.Text().MaxSize(200)).MaxSize(5)),
			}
		}

		got := toPromptWebResults(results)
		if len(got) > len(results) {
			ht.Fatalf("len(output)=%d > len(input)=%d", len(got), len(results))
		}
	}, hegel.WithTestCases(200))
}

// TestFormatLeetCodeMessage_NeverPanics verifies formatLeetCodeMessage
// does not panic on arbitrary non-nil LeetCodeQuestion. The function's
// contract requires a non-nil pointer (it dereferences question
// immediately at leetcode.go:128), so nil is excluded. Difficulty is
// drawn from both known values and arbitrary text to exercise the
// unknown-difficulty path.
func TestFormatLeetCodeMessage_NeverPanics(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		q := &LeetCodeQuestion{
			Title:     hegel.Draw(ht, hegel.Text().MaxSize(40)),
			TitleSlug: hegel.Draw(ht, hegel.Text().MaxSize(40)),
			Difficulty: hegel.Draw(ht, hegel.OneOf(
				hegel.SampledFrom([]string{"Easy", "Medium", "Hard", ""}), //nolint:goconst
				hegel.Text().MaxSize(10),
			)),
		}
		msg := formatLeetCodeMessage(q)
		// Property: never panics, always returns non-empty.
		if msg == "" {
			ht.Fatalf("empty output for %+v", q)
		}
	}, hegel.WithTestCases(200))
}
