package bot

import (
	"context"
	"strings"
	"testing"
)

func TestNewGeminiExplainer_EmptyKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"tabs and spaces", " \t "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newGeminiExplainer(context.Background(), tt.key, "")
			if err == nil {
				t.Fatal("expected error for empty/whitespace key")
			}
			if !strings.Contains(err.Error(), "API key is required") {
				t.Fatalf("expected 'API key is required' error, got %q", err.Error())
			}
		})
	}
}
