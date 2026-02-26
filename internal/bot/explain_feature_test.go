package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"google.golang.org/genai"
)

type mockContentGenerator struct {
	resp *genai.GenerateContentResponse
	err  error
}

func (m *mockContentGenerator) GenerateContent(
	context.Context,
	string,
	[]*genai.Content,
	*genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	return m.resp, m.err
}

func TestExtractQuotedText(t *testing.T) {
	t.Run("uses replied message text", func(t *testing.T) {
		msg := &models.Message{
			ReplyToMessage: &models.Message{Text: "  hello world  "},
		}
		got := extractQuotedText(msg)
		if got != "hello world" {
			t.Fatalf("expected replied text, got %q", got)
		}
	})

	t.Run("uses replied message caption when text absent", func(t *testing.T) {
		msg := &models.Message{
			ReplyToMessage: &models.Message{Caption: "  image caption  "},
		}
		got := extractQuotedText(msg)
		if got != "image caption" {
			t.Fatalf("expected caption, got %q", got)
		}
	})

	t.Run("falls back to quote text", func(t *testing.T) {
		msg := &models.Message{
			Quote: &models.TextQuote{Text: "  quoted snippet  "},
		}
		got := extractQuotedText(msg)
		if got != "quoted snippet" {
			t.Fatalf("expected quote text, got %q", got)
		}
	})

	t.Run("returns empty when nothing to explain", func(t *testing.T) {
		msg := &models.Message{}
		got := extractQuotedText(msg)
		if got != "" {
			t.Fatalf("expected empty text, got %q", got)
		}
	})
}

func TestSanitizeForPrompt(t *testing.T) {
	got := sanitizeForPrompt("  a\tb\n\"c` \x00 d  ", 100)
	want := "a b 'c' d"
	if got != want {
		t.Fatalf("sanitizeForPrompt() = %q, want %q", got, want)
	}
}

func TestGeminiExplainer_ExplainTimeout(t *testing.T) {
	explainer := &geminiExplainer{
		generator: &mockContentGenerator{err: context.DeadlineExceeded},
	}

	_, err := explainer.explainWithLanguage(context.Background(), "hello", false)
	if !errors.Is(err, ErrExplainTimeout) {
		t.Fatalf("expected ErrExplainTimeout, got %v", err)
	}
}

func TestGeminiExplainer_ExplainSuccessAndTruncation(t *testing.T) {
	longText := strings.Repeat("a", maxExplainResponseLength+200)
	explainer := &geminiExplainer{
		generator: &mockContentGenerator{
			resp: &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{
					{
						Content: &genai.Content{
							Parts: []*genai.Part{
								{Text: longText},
							},
						},
					},
				},
			},
		},
	}

	got, err := explainer.explainWithLanguage(context.Background(), "hello", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != maxExplainResponseLength {
		t.Fatalf("expected truncated length %d, got %d", maxExplainResponseLength, len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated suffix ..., got %q", got[len(got)-10:])
	}
}

func TestShouldHandleExplainMention(t *testing.T) {
	prevMention := botMention
	botMention = "@csy_helper_dev_bot"
	defer func() { botMention = prevMention }()

	t.Run("matches when mention and phrase are present", func(t *testing.T) {
		update := &models.Update{
			Message: &models.Message{
				Text: "@csy_helper_dev_bot explain me this",
				Entities: []models.MessageEntity{
					{
						Type:   models.MessageEntityTypeMention,
						Offset: 0,
						Length: len("@csy_helper_dev_bot"),
					},
				},
			},
		}
		if !shouldHandleExplainMention(update) {
			t.Fatal("expected matcher to pass")
		}
	})

	t.Run("does not match without mention", func(t *testing.T) {
		update := &models.Update{
			Message: &models.Message{
				Text: "explain me this",
			},
		}
		if shouldHandleExplainMention(update) {
			t.Fatal("expected matcher to fail")
		}
	})
}

func TestShouldRespondInBurmese(t *testing.T) {
	if shouldRespondInBurmese("မင်္ဂလာပါ @csy_helper_dev_bot explain me this") != true {
		t.Fatal("expected Burmese request to be detected")
	}
	if shouldRespondInBurmese("@csy_helper_dev_bot explain me this") != false {
		t.Fatal("expected non-Burmese request not to be detected")
	}
}
