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

func TestParseAllowedUsernames(t *testing.T) {
	t.Run("empty returns empty map", func(t *testing.T) {
		got, err := parseAllowedUsernames("  ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %d entries", len(got))
		}
	})

	t.Run("normalizes at-prefix and case", func(t *testing.T) {
		got, err := parseAllowedUsernames("@Alice, bob_99 ,")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(got))
		}
		if _, ok := got["alice"]; !ok {
			t.Fatal("expected alice in map")
		}
		if _, ok := got["bob_99"]; !ok {
			t.Fatal("expected bob_99 in map")
		}
	})

	t.Run("invalid username returns error", func(t *testing.T) {
		if _, err := parseAllowedUsernames("alice,bad name"); err == nil {
			t.Fatal("expected error for invalid username")
		}
	})
}

func TestIsAllowedUsername(t *testing.T) {
	prev := allowedUsernames
	defer func() { allowedUsernames = prev }()
	allowedUsernames = map[string]struct{}{"alice": {}}

	if !isAllowedUsername("@AlIcE") {
		t.Fatal("expected @AlIcE to be allowed")
	}
	if isAllowedUsername("bob") {
		t.Fatal("expected bob to be rejected")
	}
	if isAllowedUsername("") {
		t.Fatal("expected empty username to be rejected")
	}
}

func TestEnforcePrivateChatAccess(t *testing.T) {
	prev := allowedUsernames
	defer func() { allowedUsernames = prev }()
	allowedUsernames = map[string]struct{}{"alice": {}}

	chat := &models.Chat{ID: 42, Type: models.ChatTypePrivate}

	t.Run("allows allowlisted user", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{
			Chat: *chat,
			From: &models.User{ID: 7, Username: "Alice"},
		}}
		if !enforcePrivateChatAccess(chat, update) {
			t.Fatal("expected allowlisted user to be allowed")
		}
	})

	t.Run("rejects other user", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{
			Chat: *chat,
			From: &models.User{ID: 8, Username: "bob"},
		}}
		if enforcePrivateChatAccess(chat, update) {
			t.Fatal("expected non-allowlisted user to be rejected")
		}
	})

	t.Run("rejects user without username", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{
			Chat: *chat,
			From: &models.User{ID: 9},
		}}
		if enforcePrivateChatAccess(chat, update) {
			t.Fatal("expected user without username to be rejected")
		}
	})
}

func TestExtractUserFromUpdate(t *testing.T) {
	t.Run("extracts from message", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{From: &models.User{ID: 5}}}
		user := extractUserFromUpdate(update)
		if user == nil || user.ID != 5 {
			t.Fatalf("expected user id 5, got %+v", user)
		}
	})

	t.Run("extracts from my_chat_member", func(t *testing.T) {
		update := &models.Update{MyChatMember: &models.ChatMemberUpdated{From: models.User{ID: 6}}}
		user := extractUserFromUpdate(update)
		if user == nil || user.ID != 6 {
			t.Fatalf("expected user id 6, got %+v", user)
		}
	})

	t.Run("nil update returns nil", func(t *testing.T) {
		if extractUserFromUpdate(nil) != nil {
			t.Fatal("expected nil for nil update")
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
