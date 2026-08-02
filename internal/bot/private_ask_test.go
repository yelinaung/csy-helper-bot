package bot

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func privateTextUpdate(text string) *models.Update {
	return &models.Update{Message: &models.Message{
		Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate},
		Text: text,
	}}
}

func groupTextUpdate(text string) *models.Update {
	return &models.Update{Message: &models.Message{
		Chat: models.Chat{ID: -100, Type: models.ChatTypeSupergroup},
		Text: text,
	}}
}

func withTestBotMention(t *testing.T) {
	t.Helper()
	prev := botMention
	t.Cleanup(func() { botMention = prev })
	botMention = "@testbot"
}

func TestShouldHandleAskMentionInPrivateChat(t *testing.T) {
	withTestBotMention(t)

	t.Run("plain text needs no mention", func(t *testing.T) {
		if !shouldHandleAskMention(privateTextUpdate("what does mutex mean?")) {
			t.Fatal("expected private plain text to be handled as an ask")
		}
	})

	t.Run("mention still works", func(t *testing.T) {
		if !shouldHandleAskMention(privateTextUpdate("@testbot what does mutex mean?")) {
			t.Fatal("expected mentioned private text to be handled as an ask")
		}
	})

	t.Run("slash command is ignored", func(t *testing.T) {
		if shouldHandleAskMention(privateTextUpdate("/start")) {
			t.Fatal("expected slash command to be ignored")
		}
	})

	t.Run("blank text is ignored", func(t *testing.T) {
		if shouldHandleAskMention(privateTextUpdate("   ")) {
			t.Fatal("expected blank text to be ignored")
		}
	})

	t.Run("bare tweet link is left to the x-link handler", func(t *testing.T) {
		update := privateTextUpdate("https://x.com/someone/status/123")
		if shouldHandleAskMention(update) {
			t.Fatal("expected bare tweet link to fall through to xlink")
		}
		if !shouldHandleXLink(update) {
			t.Fatal("expected xlink handler to claim the bare tweet link")
		}
	})

	t.Run("tweet link with a question is an ask", func(t *testing.T) {
		if !shouldHandleAskMention(privateTextUpdate("what is this https://x.com/someone/status/123")) {
			t.Fatal("expected link plus question to be handled as an ask")
		}
	})

	t.Run("group text still requires a mention", func(t *testing.T) {
		if shouldHandleAskMention(groupTextUpdate("what does mutex mean?")) {
			t.Fatal("expected unmentioned group text to be ignored")
		}
	})
}

func TestExtractAskQuestionInPrivateChat(t *testing.T) {
	withTestBotMention(t)

	t.Run("whole text is the question", func(t *testing.T) {
		got := extractAskQuestion(privateTextUpdate("what does mutex mean?").Message)
		if got != "what does mutex mean?" {
			t.Fatalf("expected full text as question, got %q", got)
		}
	})

	t.Run("mention is still stripped", func(t *testing.T) {
		got := extractAskQuestion(privateTextUpdate("@testbot what does mutex mean?").Message)
		if got != "what does mutex mean?" {
			t.Fatalf("expected mention to be stripped, got %q", got)
		}
	})

	t.Run("group text without a mention yields nothing", func(t *testing.T) {
		if got := extractAskQuestion(groupTextUpdate("what does mutex mean?").Message); got != "" {
			t.Fatalf("expected empty question, got %q", got)
		}
	})
}

func TestShouldHandlePhotoAskInPrivateChat(t *testing.T) {
	withTestBotMention(t)

	privatePhoto := &models.Update{Message: &models.Message{
		Chat:    models.Chat{ID: 42, Type: models.ChatTypePrivate},
		Photo:   []models.PhotoSize{{FileID: "file"}},
		Caption: "what is this?",
	}}
	if !shouldHandlePhotoAsk(privatePhoto) {
		t.Fatal("expected private photo to be handled without a mention")
	}
	if got := extractPhotoAskQuestion(privatePhoto.Message); got != "what is this?" {
		t.Fatalf("expected caption as question, got %q", got)
	}

	privatePhotoNoCaption := &models.Update{Message: &models.Message{
		Chat:  models.Chat{ID: 42, Type: models.ChatTypePrivate},
		Photo: []models.PhotoSize{{FileID: "file"}},
	}}
	if !shouldHandlePhotoAsk(privatePhotoNoCaption) {
		t.Fatal("expected captionless private photo to be handled")
	}

	groupPhoto := &models.Update{Message: &models.Message{
		Chat:    models.Chat{ID: -100, Type: models.ChatTypeSupergroup},
		Photo:   []models.PhotoSize{{FileID: "file"}},
		Caption: "what is this?",
	}}
	if shouldHandlePhotoAsk(groupPhoto) {
		t.Fatal("expected unmentioned group photo to be ignored")
	}
}

func TestAskUsageText(t *testing.T) {
	withTestBotMention(t)

	private := askUsageText(privateTextUpdate("").Message)
	if strings.Contains(private, botMention) {
		t.Fatalf("expected private usage text to omit the mention, got %q", private)
	}
	group := askUsageText(groupTextUpdate("").Message)
	if !strings.Contains(group, "testbot") {
		t.Fatalf("expected group usage text to mention the bot, got %q", group)
	}
}
