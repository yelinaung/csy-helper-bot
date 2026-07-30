package bot

import (
	"context"
	"strings"
	"testing"
)

func TestExplainWithExtractResults_RequiresResults(t *testing.T) {
	explainer := &geminiExplainer{generator: &mockContentGenerator{}}

	_, err := explainer.explainWithExtractResults(context.Background(), "", "question", nil, false)
	if err == nil || !strings.Contains(err.Error(), "extract results") {
		t.Fatalf("expected extract results error, got %v", err)
	}
}

func TestExplainWithExtractResults_NilExplainer(t *testing.T) {
	var explainer *geminiExplainer

	results := []parallelExtractResult{
		{URL: "https://example.com/explain-a", Title: "Sample Title", Excerpts: []string{"excerpt"}},
	}
	_, err := explainer.explainWithExtractResults(context.Background(), "", "question", results, false)
	if err == nil || !strings.Contains(err.Error(), "gemini client not initialized") {
		t.Fatalf("expected not initialized error, got %v", err)
	}
}

func TestExplainWithExtractResults_RequiresTextOrQuestion(t *testing.T) {
	explainer := &geminiExplainer{generator: &mockContentGenerator{}}

	results := []parallelExtractResult{
		{URL: "https://example.com/explain-a", Title: "Sample Title", Excerpts: []string{"excerpt"}},
	}
	_, err := explainer.explainWithExtractResults(context.Background(), "", "", results, false)
	if err == nil || !strings.Contains(err.Error(), "text or question is required") {
		t.Fatalf("expected text or question error, got %v", err)
	}
}

func TestExplainWithExtractResults_Success(t *testing.T) {
	generator := &capturingGenerator{}
	explainer := &geminiExplainer{generator: generator}

	results := []parallelExtractResult{
		{URL: "https://example.com/pro-plan", Title: "Pricing", PublishDate: "2026-01-01", Excerpts: []string{"The Pro plan costs $10/month."}},
	}
	out, err := explainer.explainWithExtractResults(context.Background(), "", "what are the plan prices?", results, false)
	if err != nil {
		t.Fatalf("explainWithExtractResults() error = %v", err)
	}
	if !strings.HasPrefix(out, "explanation") {
		t.Errorf("unexpected output %q", out)
	}

	prompt := generator.capturedContents[0].Parts[0].Text
	for _, want := range []string{
		`"web_results"`,
		"https://example.com/pro-plan",
		`"Pricing"`,
		"The Pro plan costs $10/month.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildExtractExplainPrompt(t *testing.T) {
	prompt, err := buildExtractExplainPrompt(&buildExplainPromptRequest{
		Nonce:               "abcd1234",
		Question:            "what are the plan prices?",
		LanguageInstruction: "Respond in English.",
		Tone:                "custom-tone",
		WebResults: []promptWebResult{
			{
				Title:       "Pricing",
				URL:         "https://example.com/pro-plan",
				PublishDate: "2026-01-01",
				Excerpts:    []string{"The Pro plan costs $10/month."},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildExtractExplainPrompt() error = %v", err)
	}

	for _, want := range []string{
		explainPromptPayloadMarker,
		`"web_results"`,
		"https://example.com/pro-plan",
		"The Pro plan costs $10/month.",
		"abcd1234",
		"custom-tone",
		"extracted",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildExtractExplainPrompt_NilRequest(t *testing.T) {
	if _, err := buildExtractExplainPrompt(nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}
