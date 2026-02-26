package bot

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestParseAllowedGroupIDs(t *testing.T) {
	t.Run("empty returns empty map", func(t *testing.T) {
		got, err := parseAllowedGroupIDs("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %d entries", len(got))
		}
	})

	t.Run("parses comma separated ids", func(t *testing.T) {
		got, err := parseAllowedGroupIDs("-100123, -99")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := got[-100123]; !ok {
			t.Fatal("expected -100123 in map")
		}
		if _, ok := got[-99]; !ok {
			t.Fatal("expected -99 in map")
		}
	})

	t.Run("invalid id returns error", func(t *testing.T) {
		_, err := parseAllowedGroupIDs("-100123,abc")
		if err == nil {
			t.Fatal("expected error for invalid group id")
		}
	})
}

func TestExtractChatFromUpdate(t *testing.T) {
	t.Run("extracts from message", func(t *testing.T) {
		update := &models.Update{
			Message: &models.Message{
				Chat: models.Chat{ID: -1001, Type: models.ChatTypeGroup},
			},
		}
		chat := extractChatFromUpdate(update)
		if chat == nil || chat.ID != -1001 {
			t.Fatalf("expected chat id -1001, got %+v", chat)
		}
	})

	t.Run("extracts from my_chat_member", func(t *testing.T) {
		update := &models.Update{
			MyChatMember: &models.ChatMemberUpdated{
				Chat: models.Chat{ID: -1002, Type: models.ChatTypeSupergroup},
			},
		}
		chat := extractChatFromUpdate(update)
		if chat == nil || chat.ID != -1002 {
			t.Fatalf("expected chat id -1002, got %+v", chat)
		}
	})
}

func TestIsGroupLikeChat(t *testing.T) {
	if !isGroupLikeChat(models.ChatTypeGroup) {
		t.Fatal("expected group to be group-like")
	}
	if !isGroupLikeChat(models.ChatTypeSupergroup) {
		t.Fatal("expected supergroup to be group-like")
	}
	if isGroupLikeChat(models.ChatTypePrivate) {
		t.Fatal("private should not be group-like")
	}
}
