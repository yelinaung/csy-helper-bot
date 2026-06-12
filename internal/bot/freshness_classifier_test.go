package bot

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// capturingJSONGenerator captures GenerateContent arguments and returns a
// fixed JSON body, for testing the structured classifier call.
type capturingJSONGenerator struct {
	jsonBody         string
	capturedContents []*genai.Content
	capturedConfig   *genai.GenerateContentConfig
}

func (c *capturingJSONGenerator) GenerateContent(
	_ context.Context,
	_ string,
	contents []*genai.Content,
	config *genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	c.capturedContents = contents
	c.capturedConfig = config
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []*genai.Part{{Text: c.jsonBody}}}},
		},
	}, nil
}

func TestClassifySearchNeed_NeedsSearch(t *testing.T) {
	generator := &capturingJSONGenerator{
		jsonBody: `{"needs_search": true, "objective": "find latest Go release", "search_queries": ["go latest release", "golang new version 2026"]}`,
	}
	explainer := &geminiExplainer{generator: generator}

	plan, err := explainer.classifySearchNeed(context.Background(), "", "what is the latest Go version?")
	if err != nil {
		t.Fatalf("classifySearchNeed() error = %v", err)
	}

	if !plan.NeedsSearch {
		t.Error("expected NeedsSearch true")
	}
	if plan.Objective != "find latest Go release" {
		t.Errorf("objective = %q", plan.Objective)
	}
	if len(plan.SearchQueries) != 2 {
		t.Errorf("search_queries = %v", plan.SearchQueries)
	}

	if generator.capturedConfig.ResponseMIMEType != "application/json" {
		t.Errorf("ResponseMIMEType = %q, want application/json", generator.capturedConfig.ResponseMIMEType)
	}
	if generator.capturedConfig.ResponseSchema == nil {
		t.Error("expected ResponseSchema to be set")
	}

	prompt := generator.capturedContents[0].Parts[0].Text
	if !strings.Contains(prompt, explainPromptPayloadMarker) {
		t.Error("prompt missing untrusted payload marker")
	}
	if !strings.Contains(prompt, "what is the latest Go version?") {
		t.Error("prompt missing question")
	}
}

func TestClassifySearchNeed_NoSearch(t *testing.T) {
	explainer := &geminiExplainer{
		generator: &capturingJSONGenerator{jsonBody: `{"needs_search": false}`},
	}

	plan, err := explainer.classifySearchNeed(context.Background(), "explain recursion", "")
	if err != nil {
		t.Fatalf("classifySearchNeed() error = %v", err)
	}
	if plan.NeedsSearch {
		t.Error("expected NeedsSearch false")
	}
}

func TestClassifySearchNeed_MalformedJSON(t *testing.T) {
	explainer := &geminiExplainer{
		generator: &capturingJSONGenerator{jsonBody: "not json"},
	}

	_, err := explainer.classifySearchNeed(context.Background(), "", "question")
	if err == nil || !strings.Contains(err.Error(), "decode classifier response") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestClassifySearchNeed_EmptyInput(t *testing.T) {
	explainer := &geminiExplainer{
		generator: &capturingJSONGenerator{jsonBody: `{"needs_search": false}`},
	}

	_, err := explainer.classifySearchNeed(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestNormalizeSearchPlan(t *testing.T) {
	tests := []struct {
		name          string
		plan          searchPlan
		message       string
		question      string
		wantObjective string
		wantQueries   []string
	}{
		{
			name:          "fills objective from question",
			plan:          searchPlan{NeedsSearch: true},
			question:      "latest bitcoin price?",
			wantObjective: "latest bitcoin price?",
			wantQueries:   []string{"latest bitcoin price?"},
		},
		{
			name:          "fills objective from message when question empty",
			plan:          searchPlan{NeedsSearch: true},
			message:       "ETH hits new high",
			wantObjective: "ETH hits new high",
			wantQueries:   []string{"ETH hits new high"},
		},
		{
			name: "trims and caps queries",
			plan: searchPlan{
				NeedsSearch:   true,
				Objective:     "obj",
				SearchQueries: []string{" a ", "", "b", "c", "d"},
			},
			wantObjective: "obj",
			wantQueries:   []string{"a", "b", "c"},
		},
		{
			name:          "no search leaves plan untouched",
			plan:          searchPlan{NeedsSearch: false},
			wantObjective: "",
			wantQueries:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizeSearchPlan(&tt.plan, tt.message, tt.question)
			if tt.plan.Objective != tt.wantObjective {
				t.Errorf("objective = %q, want %q", tt.plan.Objective, tt.wantObjective)
			}
			if len(tt.plan.SearchQueries) != len(tt.wantQueries) {
				t.Fatalf("queries = %v, want %v", tt.plan.SearchQueries, tt.wantQueries)
			}
			for i := range tt.wantQueries {
				if tt.plan.SearchQueries[i] != tt.wantQueries[i] {
					t.Errorf("queries[%d] = %q, want %q", i, tt.plan.SearchQueries[i], tt.wantQueries[i])
				}
			}
		})
	}
}

func TestBuildGroundedExplainPrompt(t *testing.T) {
	prompt, err := buildGroundedExplainPrompt(&buildExplainPromptRequest{
		Nonce:               "abcd1234",
		Question:            "what is the latest Go version?",
		LanguageInstruction: "Respond in English.",
		Tone:                "direct",
		Today:               "2026-06-12",
		WebResults: []promptWebResult{
			{
				Title:       "Go 1.27 released",
				URL:         "https://example.com/go",
				PublishDate: "2026-06-01",
				Excerpts:    []string{"Go 1.27 ships with faster GC."},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildGroundedExplainPrompt() error = %v", err)
	}

	for _, want := range []string{
		explainPromptPayloadMarker,
		`"web_results"`,
		"https://example.com/go",
		"Go 1.27 ships with faster GC.",
		"Today's date is 2026-06-12",
		"abcd1234",
		"direct",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestExplainWithSearchResults_RequiresResults(t *testing.T) {
	explainer := &geminiExplainer{generator: &mockContentGenerator{}}

	_, err := explainer.explainWithSearchResults(context.Background(), "", "question", nil, false)
	if err == nil || !strings.Contains(err.Error(), "search results") {
		t.Fatalf("expected search results error, got %v", err)
	}
}

func TestExplainWithSearchResults_Success(t *testing.T) {
	generator := &capturingGenerator{}
	explainer := &geminiExplainer{generator: generator}

	results := []parallelSearchResult{
		{URL: "https://example.com/latest", Title: "News", Excerpts: []string{"excerpt"}},
	}
	out, err := explainer.explainWithSearchResults(context.Background(), "", "latest news?", results, false)
	if err != nil {
		t.Fatalf("explainWithSearchResults() error = %v", err)
	}
	if !strings.HasPrefix(out, "explanation") {
		t.Errorf("unexpected output %q", out)
	}

	prompt := generator.capturedContents[0].Parts[0].Text
	if !strings.Contains(prompt, `"web_results"`) {
		t.Error("prompt missing web_results payload")
	}
}
