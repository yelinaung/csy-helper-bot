package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

const (
	geminiModelName          = "gemini-2.5-flash"
	explainTimeout           = 15 * time.Second
	maxExplainInputLength    = 1500
	maxExplainResponseLength = 3500
)

var ErrExplainTimeout = errors.New("explain request timed out")

type geminiContentGenerator interface {
	GenerateContent(
		ctx context.Context,
		model string,
		contents []*genai.Content,
		config *genai.GenerateContentConfig,
	) (*genai.GenerateContentResponse, error)
}

type geminiExplainer struct {
	generator geminiContentGenerator
}

func newGeminiExplainer(ctx context.Context, apiKey string) (*geminiExplainer, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("gemini API key is required")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &geminiExplainer{
		generator: client.Models,
	}, nil
}

func (g *geminiExplainer) explain(ctx context.Context, text string) (string, error) {
	if g == nil || g.generator == nil {
		return "", errors.New("gemini client not initialized")
	}

	sanitized := sanitizeForPrompt(text, maxExplainInputLength)
	if sanitized == "" {
		return "", errors.New("text is required")
	}

	prompt := fmt.Sprintf(`Explain the following message in simple terms.
Keep it concise and practical. Use plain language.

Message:
"%s"`, sanitized)

	timeoutCtx, cancel := context.WithTimeout(ctx, explainTimeout)
	defer cancel()

	temp := float32(0.2)
	config := &genai.GenerateContentConfig{
		Temperature:     &temp,
		MaxOutputTokens: 600,
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: "You explain technical and non-technical text clearly and briefly. Avoid fluff."},
			},
		},
	}

	resp, err := g.generator.GenerateContent(timeoutCtx, geminiModelName, []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				{Text: prompt},
			},
		},
	}, config)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", ErrExplainTimeout
		}
		return "", fmt.Errorf("gemini generate content failed: %w", err)
	}
	if resp == nil {
		return "", errors.New("empty response from Gemini")
	}

	out := strings.TrimSpace(resp.Text())
	if out == "" {
		return "", errors.New("empty explanation from Gemini")
	}

	if len(out) > maxExplainResponseLength {
		out = strings.TrimSpace(out[:maxExplainResponseLength-3]) + "..."
	}

	return out, nil
}

func sanitizeForPrompt(input string, maxLength int) string {
	input = strings.ReplaceAll(input, `"`, `'`)
	input = strings.ReplaceAll(input, "`", "'")
	input = strings.ReplaceAll(input, "\x00", "")
	input = strings.Join(strings.Fields(input), " ")

	if len(input) > maxLength {
		input = strings.TrimSpace(input[:maxLength])
	}

	return input
}
