package bot

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestExtractFixedXLinks(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "rewrites x.com status link",
			text: "check this https://x.com/foo/status/123",
			want: []string{"https://fixupx.com/foo/status/123"},
		},
		{
			name: "rewrites twitter.com status link",
			text: "https://twitter.com/bar/status/456",
			want: []string{"https://fxtwitter.com/bar/status/456"},
		},
		{
			name: "strips www and mobile subdomains",
			text: "https://www.x.com/a/status/1 and https://mobile.twitter.com/b/status/2",
			want: []string{"https://fixupx.com/a/status/1", "https://fxtwitter.com/b/status/2"},
		},
		{
			name: "drops query and fragment",
			text: "https://x.com/foo/status/123?s=20&t=abc#xyz",
			want: []string{"https://fixupx.com/foo/status/123"},
		},
		{
			name: "preserves i/web/status form",
			text: "https://twitter.com/i/web/status/789",
			want: []string{"https://fxtwitter.com/i/web/status/789"},
		},
		{
			name: "trims trailing punctuation",
			text: "see (https://x.com/foo/status/123).",
			want: []string{"https://fixupx.com/foo/status/123"},
		},
		{
			name: "dedupes repeated links",
			text: "https://x.com/foo/status/1 https://x.com/foo/status/1",
			want: []string{"https://fixupx.com/foo/status/1"},
		},
		{
			name: "ignores profile links without status",
			text: "https://x.com/foo",
			want: nil,
		},
		{
			name: "ignores non-x hosts",
			text: "https://example.com/foo/status/1",
			want: nil,
		},
		{
			name: "ignores already-fixed links",
			text: "https://fixupx.com/foo/status/1",
			want: nil,
		},
		{
			name: "no links",
			text: "just some plain text",
			want: nil,
		},
		{
			name: "caps at maxXLinksPerMessage",
			text: "https://x.com/a/status/1 https://x.com/a/status/2 https://x.com/a/status/3 " +
				"https://x.com/a/status/4 https://x.com/a/status/5 https://x.com/a/status/6",
			want: []string{
				"https://fixupx.com/a/status/1",
				"https://fixupx.com/a/status/2",
				"https://fixupx.com/a/status/3",
				"https://fixupx.com/a/status/4",
				"https://fixupx.com/a/status/5",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFixedXLinks(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("extractFixedXLinks(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("link[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestShouldHandleXLink(t *testing.T) {
	tests := []struct {
		name   string
		update *models.Update
		want   bool
	}{
		{
			name:   "nil update",
			update: nil,
			want:   false,
		},
		{
			name:   "nil message",
			update: &models.Update{},
			want:   false,
		},
		{
			name:   "empty text",
			update: &models.Update{Message: &models.Message{Text: "   "}},
			want:   false,
		},
		{
			name:   "plain text without link",
			update: &models.Update{Message: &models.Message{Text: "hello world"}},
			want:   false,
		},
		{
			name:   "message with tweet link",
			update: &models.Update{Message: &models.Message{Text: "look https://x.com/foo/status/1"}},
			want:   true,
		},
		{
			name:   "profile link only",
			update: &models.Update{Message: &models.Message{Text: "https://x.com/foo"}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldHandleXLink(tt.update); got != tt.want {
				t.Errorf("shouldHandleXLink() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractFixedXLinks_MultipleJoinable(t *testing.T) {
	links := extractFixedXLinks("https://x.com/a/status/1 https://twitter.com/b/status/2")
	joined := strings.Join(links, "\n")
	want := "https://fixupx.com/a/status/1\nhttps://fxtwitter.com/b/status/2"
	if joined != want {
		t.Errorf("joined = %q, want %q", joined, want)
	}
}
