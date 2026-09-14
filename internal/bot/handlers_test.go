package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	dbn "github.com/NimbleMarkets/dbn-go"
	dbn_hist "github.com/NimbleMarkets/dbn-go/hist"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"

	appotel "gitlab.com/yelinaung/csy-helper-bot/internal/otel"
)

const (
	mockChatID        = int64(-1001)
	mockPrivateChatID = int64(42)
)

// mockTelegramServer is a configurable Telegram Bot API stub. Unlike
// testBotServer it also answers getFile and the /file/ download path, and it
// can fail every call whose path contains a configured prefix.
type mockTelegramServer struct {
	mu            sync.Mutex
	paths         []string
	lastText      string
	filePath      string
	fileContent   []byte
	failPrefixes  []string
	responseCount int
}

func (s *mockTelegramServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	fail := false
	for _, p := range s.failPrefixes {
		if strings.Contains(r.URL.Path, p) {
			fail = true
			break
		}
	}
	filePath := s.filePath
	fileContent := s.fileContent
	s.mu.Unlock()

	if fail {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "bad request"})
		return
	}

	if strings.Contains(r.URL.Path, "/file/") {
		content := fileContent
		if content == nil {
			content = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(content)
		return
	}

	if strings.Contains(r.URL.Path, "getFile") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"file_id":   "file-1",
				"file_path": filePath,
			},
		})
		return
	}

	const maxFormSize = 1 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxFormSize)
	if err := r.ParseMultipartForm(maxFormSize); err == nil { //nolint:gosec,nolintlint
		if txt := r.FormValue("text"); txt != "" {
			s.mu.Lock()
			s.lastText = txt
			s.responseCount++
			s.mu.Unlock()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true,
		"result": map[string]any{
			"message_id": 1,
			"chat":       map[string]any{"id": mockChatID, "type": "group"},
			"date":       1234567890,
		},
	})
}

func (s *mockTelegramServer) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastText
}

func (s *mockTelegramServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func newMockBot(t *testing.T, srv *mockTelegramServer) *bot.Bot {
	t.Helper()
	server := httptest.NewTestServer(t, http.HandlerFunc(srv.ServeHTTP))
	server.Start()

	b, err := bot.New("dummy:test-token", bot.WithServerURL(server.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("create mock bot: %v", err)
	}
	return b
}

func withTextExplainer(t *testing.T, generator geminiContentGenerator) {
	t.Helper()
	prev := textExplainer
	t.Cleanup(func() { textExplainer = prev })
	textExplainer = &geminiExplainer{generator: generator}
}

func messageUpdate(text string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID:   1,
		Chat: models.Chat{ID: mockChatID, Type: models.ChatTypeSupergroup},
		Text: text,
	}}
}

func privateMessageUpdate(text string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID:   1,
		Chat: models.Chat{ID: mockPrivateChatID, Type: models.ChatTypePrivate},
		Text: text,
	}}
}

func TestStartHandler(t *testing.T) {
	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	startHandler(context.Background(), b, messageUpdate("/start"))

	if !strings.Contains(srv.last(), "Welcome") {
		t.Fatalf("expected welcome message, got %q", srv.last())
	}
}

func TestHelpHandler(t *testing.T) {
	withTestBotMention(t)
	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	helpHandler(context.Background(), b, messageUpdate("/help"))

	got := srv.last()
	if !strings.Contains(got, "/start") || !strings.Contains(got, "testbot") {
		t.Fatalf("unexpected help text: %q", got)
	}
}

func TestLC_Handler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"activeDailyCodingChallengeQuestion":{"question":{"title":"Two Sum","titleSlug":"two-sum","difficulty":"Easy"}}}}`))
		}))
		server.Start()
		useRedirectedHTTPClient(t, server.URL)

		srv := &mockTelegramServer{}
		b := newMockBot(t, srv)
		lcHandler(context.Background(), b, messageUpdate("!lc"))

		if !strings.Contains(srv.last(), "Two Sum") {
			t.Fatalf("expected leetcode title, got %q", srv.last())
		}
	})

	t.Run("error", func(t *testing.T) {
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		server.Start()
		useRedirectedHTTPClient(t, server.URL)

		srv := &mockTelegramServer{}
		b := newMockBot(t, srv)
		lcHandler(context.Background(), b, messageUpdate("!lc"))

		if !strings.Contains(srv.last(), "Failed to fetch LeetCode") {
			t.Fatalf("expected failure message, got %q", srv.last())
		}
	})
}

func TestXLinkHandler(t *testing.T) {
	t.Run("rewrites links", func(t *testing.T) {
		srv := &mockTelegramServer{}
		b := newMockBot(t, srv)
		update := messageUpdate("look https://x.com/user/status/123")

		xLinkHandler(context.Background(), b, update)

		if !strings.Contains(srv.last(), "fixupx.com/user/status/123") {
			t.Fatalf("expected rewritten link, got %q", srv.last())
		}
	})

	t.Run("no links is a no-op", func(t *testing.T) {
		srv := &mockTelegramServer{}
		b := newMockBot(t, srv)

		xLinkHandler(context.Background(), b, messageUpdate("no links here"))

		if srv.count() != 0 {
			t.Fatalf("expected no API calls, got %d", srv.count())
		}
	})
}

func TestObsMiddlewareChain(t *testing.T) {
	prev := botMention
	t.Cleanup(func() { botMention = prev })
	botMention = "@testbot"

	prevGroups := allowedGroups
	t.Cleanup(func() { allowedGroups = prevGroups })
	allowedGroups = map[int64]struct{}{mockChatID: {}}

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	called := false
	handler := obs("bot.test", "/test")(func(context.Context, *bot.Bot, *models.Update) {
		called = true
		appotel.RecordOutcome(context.Background(), "success")
	})

	handler(context.Background(), b, messageUpdate("/test"))

	if !called {
		t.Fatal("expected wrapped handler to be called")
	}
}

func TestTracingMiddleware_ErrorOutcome(t *testing.T) {
	called := false
	handler := tracingMiddleware("bot.err", "", func(ctx context.Context, _ *bot.Bot, _ *models.Update) {
		called = true
		appotel.RecordOutcome(ctx, "error")
	})

	handler(context.Background(), nil, nil)

	if !called {
		t.Fatal("expected wrapped handler to be called")
	}
}

func TestApplyUpdateAttributes(t *testing.T) {
	t.Run("nil update", func(t *testing.T) {
		span := trace.SpanFromContext(context.Background())
		requireNotPanics(t, func() { applyUpdateAttributes(span, "bot.x", "", nil) })
	})

	t.Run("message with sender", func(t *testing.T) {
		update := messageUpdate("hi")
		update.ID = 9
		update.Message.From = &models.User{ID: 7}
		span := trace.SpanFromContext(context.Background())
		requireNotPanics(t, func() { applyUpdateAttributes(span, "bot.x", "/x", update) })
	})
}

func requireNotPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	fn()
}

func TestEnforceChatAccess(t *testing.T) {
	prevGroups := allowedGroups
	t.Cleanup(func() { allowedGroups = prevGroups })
	allowedGroups = map[int64]struct{}{mockChatID: {}}

	t.Run("nil chat is allowed", func(t *testing.T) {
		if !enforceChatAccess(context.Background(), nil, &models.Update{}) {
			t.Fatal("expected nil chat to be allowed")
		}
	})

	t.Run("allowed group is allowed", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: mockChatID, Type: models.ChatTypeSupergroup}}}
		if !enforceChatAccess(context.Background(), nil, update) {
			t.Fatal("expected allowed group to pass")
		}
	})

	t.Run("unauthorized group leaves and denies", func(t *testing.T) {
		srv := &mockTelegramServer{}
		b := newMockBot(t, srv)
		update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: -777, Type: models.ChatTypeSupergroup}}}

		if enforceChatAccess(context.Background(), b, update) {
			t.Fatal("expected unauthorized group to be denied")
		}
		if srv.count() == 0 {
			t.Fatal("expected LeaveChat API call")
		}
	})

	t.Run("non-group chat is denied", func(t *testing.T) {
		update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: -5, Type: models.ChatTypeChannel}}}
		if enforceChatAccess(context.Background(), nil, update) {
			t.Fatal("expected non-group chat to be denied")
		}
	})

	t.Run("private allowlisted user is allowed", func(t *testing.T) {
		prevUsers := allowedUsernames
		t.Cleanup(func() { allowedUsernames = prevUsers })
		allowedUsernames = map[string]struct{}{"alice": {}}

		update := &models.Update{Message: &models.Message{
			Chat: models.Chat{ID: mockPrivateChatID, Type: models.ChatTypePrivate},
			From: &models.User{ID: 7, Username: "Alice"},
		}}
		if !enforceChatAccess(context.Background(), nil, update) {
			t.Fatal("expected allowlisted private user to pass")
		}
	})
}

func TestStartAllowedGroupsReporter_CanceledContext(t *testing.T) {
	prevGroups := allowedGroups
	t.Cleanup(func() { allowedGroups = prevGroups })
	allowedGroups = map[int64]struct{}{mockChatID: {}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		startAllowedGroupsReporter(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reporter did not stop after context cancellation")
	}
}

func TestLogAllowedUsernames(t *testing.T) {
	prevUsers := allowedUsernames
	t.Cleanup(func() { allowedUsernames = prevUsers })
	allowedUsernames = map[string]struct{}{"alice": {}, "bob": {}}

	requireNotPanics(t, func() { logAllowedUsernames("test heartbeat") })
}

func TestExtractChatFromUpdate_AllKinds(t *testing.T) {
	chat := func(id int64) models.Chat { return models.Chat{ID: id} }
	cases := []struct {
		name   string
		update *models.Update
		wantID int64
	}{
		{"edited message", &models.Update{EditedMessage: &models.Message{Chat: chat(-2)}}, -2},
		{"channel post", &models.Update{ChannelPost: &models.Message{Chat: chat(-3)}}, -3},
		{"edited channel post", &models.Update{EditedChannelPost: &models.Message{Chat: chat(-4)}}, -4},
		{"my chat member", &models.Update{MyChatMember: &models.ChatMemberUpdated{Chat: chat(-5)}}, -5},
		{"chat member", &models.Update{ChatMember: &models.ChatMemberUpdated{Chat: chat(-6)}}, -6},
		{"chat join request", &models.Update{ChatJoinRequest: &models.ChatJoinRequest{Chat: chat(-7)}}, -7},
		{"callback query", &models.Update{CallbackQuery: &models.CallbackQuery{Message: models.MaybeInaccessibleMessage{
			Type:    models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{Chat: chat(-8)},
		}}}, -8},
		{"empty", &models.Update{}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractChatFromUpdate(tc.update)
			if tc.wantID == 0 {
				if got != nil {
					t.Fatalf("expected nil chat, got %+v", got)
				}
				return
			}
			if got == nil || got.ID != tc.wantID {
				t.Fatalf("expected chat id %d, got %+v", tc.wantID, got)
			}
		})
	}

	if got := extractChatFromUpdate(nil); got != nil {
		t.Fatal("expected nil for nil update")
	}
}

func TestExtractUserFromUpdate_AllKinds(t *testing.T) {
	ptr := func(id int64) *models.User { return &models.User{ID: id} }
	cases := []struct {
		name   string
		update *models.Update
		wantID int64
	}{
		{"edited message", &models.Update{EditedMessage: &models.Message{From: ptr(2)}}, 2},
		{"channel post", &models.Update{ChannelPost: &models.Message{From: ptr(3)}}, 3},
		{"edited channel post", &models.Update{EditedChannelPost: &models.Message{From: ptr(4)}}, 4},
		{"my chat member", &models.Update{MyChatMember: &models.ChatMemberUpdated{From: models.User{ID: 5}}}, 5},
		{"chat member", &models.Update{ChatMember: &models.ChatMemberUpdated{From: models.User{ID: 6}}}, 6},
		{"chat join request", &models.Update{ChatJoinRequest: &models.ChatJoinRequest{From: models.User{ID: 7}}}, 7},
		{"callback query", &models.Update{CallbackQuery: &models.CallbackQuery{From: models.User{ID: 8}}}, 8},
		{"empty", &models.Update{}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUserFromUpdate(tc.update)
			if tc.wantID == 0 {
				if got != nil {
					t.Fatalf("expected nil user, got %+v", got)
				}
				return
			}
			if got == nil || got.ID != tc.wantID {
				t.Fatalf("expected user id %d, got %+v", tc.wantID, got)
			}
		})
	}

	if got := extractUserFromUpdate(nil); got != nil {
		t.Fatal("expected nil for nil update")
	}
}

func TestAskHandler_NotConfigured(t *testing.T) {
	prev := textExplainer
	textExplainer = nil
	t.Cleanup(func() { textExplainer = prev })

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	askHandler(context.Background(), b, privateMessageUpdate("what is a mutex?"))

	if !strings.Contains(srv.last(), "not configured") {
		t.Fatalf("expected not-configured message, got %q", srv.last())
	}
}

func TestAskHandler_UsageText(t *testing.T) {
	withTextExplainer(t, &mockContentGenerator{resp: emptyTextResponse()})

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	askHandler(context.Background(), b, privateMessageUpdate("ask"))

	if !strings.Contains(srv.last(), "Send me your question") {
		t.Fatalf("expected usage text, got %q", srv.last())
	}
}

func TestAskHandler_Success(t *testing.T) {
	withTextExplainer(t, &capturingGenerator{})

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	askHandler(context.Background(), b, privateMessageUpdate("what is a mutex?"))

	if !strings.Contains(srv.last(), "explanation") {
		t.Fatalf("expected explanation, got %q", srv.last())
	}
}

func TestAskHandler_RateLimited(t *testing.T) {
	withTextExplainer(t, &capturingGenerator{})

	prevLimiter := explainLimiter
	limiter := newMemoryRateLimiter(1, time.Minute)
	limiter.allow(buildExplainRateKey(mockPrivateChatID, 77), time.Now())
	explainLimiter = limiter
	t.Cleanup(func() { explainLimiter = prevLimiter })

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	update := privateMessageUpdate("what is a mutex?")
	update.Message.From = &models.User{ID: 77}
	askHandler(context.Background(), b, update)

	if !strings.Contains(srv.last(), "Rate limit reached") {
		t.Fatalf("expected rate-limit message, got %q", srv.last())
	}
}

func TestAskHandler_ExplainError(t *testing.T) {
	withTextExplainer(t, &mockContentGenerator{err: errors.New("boom")})

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	askHandler(context.Background(), b, privateMessageUpdate("what is a mutex?"))

	if !strings.Contains(srv.last(), "Failed to answer your question") {
		t.Fatalf("expected failure message, got %q", srv.last())
	}
}

func TestAskHandler_Blocked(t *testing.T) {
	withTextExplainer(t, &mockContentGenerator{resp: &genai.GenerateContentResponse{
		PromptFeedback: &genai.GenerateContentResponsePromptFeedback{BlockReason: genai.BlockedReasonSafety},
	}})

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	askHandler(context.Background(), b, privateMessageUpdate("what is a mutex?"))

	if !strings.Contains(srv.last(), "I can't answer that request") {
		t.Fatalf("expected blocked message, got %q", srv.last())
	}
}

func emptyTextResponse() *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{Content: &genai.Content{Parts: []*genai.Part{{Text: "explanation"}}}},
		},
	}
}

func TestPhotoAskHandler_NotConfigured(t *testing.T) {
	prev := textExplainer
	textExplainer = nil
	t.Cleanup(func() { textExplainer = prev })

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	photoAskHandler(context.Background(), b, photoUpdate())

	if !strings.Contains(srv.last(), "not configured") {
		t.Fatalf("expected not-configured message, got %q", srv.last())
	}
}

func TestPhotoAskHandler_Success(t *testing.T) {
	withTextExplainer(t, &capturingGenerator{})

	srv := &mockTelegramServer{filePath: "photos/pic.jpg"}
	b := newMockBot(t, srv)

	photoAskHandler(context.Background(), b, photoUpdate())

	if !strings.Contains(srv.last(), "explanation") {
		t.Fatalf("expected explanation, got %q", srv.last())
	}
}

func TestPhotoAskHandler_DownloadError(t *testing.T) {
	withTextExplainer(t, &capturingGenerator{})

	srv := &mockTelegramServer{filePath: ""}
	b := newMockBot(t, srv)

	photoAskHandler(context.Background(), b, photoUpdate())

	if !strings.Contains(srv.last(), "Failed to download the image") {
		t.Fatalf("expected download failure message, got %q", srv.last())
	}
}

func photoUpdate() *models.Update {
	return &models.Update{Message: &models.Message{
		ID:      1,
		Chat:    models.Chat{ID: mockPrivateChatID, Type: models.ChatTypePrivate},
		From:    &models.User{ID: 7},
		Photo:   []models.PhotoSize{{FileID: "file-1", Width: 1280, Height: 720}},
		Caption: "what is this?",
	}}
}

func TestAnswerAskQuestion_Photo(t *testing.T) {
	withTextExplainer(t, &capturingGenerator{})

	srv := &mockTelegramServer{filePath: "photos/pic.jpg"}
	b := newMockBot(t, srv)
	photo := &models.PhotoSize{FileID: "file-1"}

	got, err := answerAskQuestion(context.Background(), b, &models.Message{}, photo, "", "what is this?", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "explanation") {
		t.Fatalf("expected explanation, got %q", got)
	}

	got, err = answerAskQuestion(context.Background(), b, &models.Message{}, photo, "quoted context", "what is this?", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "explanation") {
		t.Fatalf("expected explanation, got %q", got)
	}
}

func TestAnswerAskQuestion_PhotoDownloadFailure(t *testing.T) {
	withTextExplainer(t, &capturingGenerator{})

	srv := &mockTelegramServer{filePath: ""}
	b := newMockBot(t, srv)

	_, err := answerAskQuestion(context.Background(), b, &models.Message{}, &models.PhotoSize{FileID: "file-1"}, "", "", false)
	if !errors.Is(err, errAskPhotoDownload) {
		t.Fatalf("expected errAskPhotoDownload, got %v", err)
	}
}

func TestSendOrEditExplainResult(t *testing.T) {
	update := &models.Update{Message: &models.Message{ID: 1, Chat: models.Chat{ID: mockChatID}}}

	t.Run("edits thinking message", func(t *testing.T) {
		b, srv := newTestBot(t)
		sendOrEditExplainResult(context.Background(), b, update, &models.Message{ID: 1}, nil, "**answer**")
		if !strings.Contains(srv.lastMessage, "answer") {
			t.Fatalf("expected edited answer, got %q", srv.lastMessage)
		}
	})

	t.Run("falls back to plain edit", func(t *testing.T) {
		b, srv := newTestBot(t)
		srv.failNextEdit = true
		sendOrEditExplainResult(context.Background(), b, update, &models.Message{ID: 1}, nil, "**answer**")
		if !strings.Contains(srv.lastMessage, "answer") {
			t.Fatalf("expected fallback answer, got %q", srv.lastMessage)
		}
	})

	t.Run("sends when thinking failed", func(t *testing.T) {
		b, srv := newTestBot(t)
		sendOrEditExplainResult(context.Background(), b, update, nil, errors.New("no thinking msg"), "answer")
		if !strings.Contains(srv.lastMessage, "answer") {
			t.Fatalf("expected sent answer, got %q", srv.lastMessage)
		}
	})

	t.Run("falls back to plain send", func(t *testing.T) {
		b, srv := newTestBot(t)
		srv.failNextSend = true
		sendOrEditExplainResult(context.Background(), b, update, nil, errors.New("no thinking msg"), "answer")
		if !strings.Contains(srv.lastMessage, "answer") {
			t.Fatalf("expected fallback sent answer, got %q", srv.lastMessage)
		}
	})
}

func TestExplainErrorToUserText(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"timeout", ErrExplainTimeout, "timed out"},
		{"blocked", ErrExplainBlocked, "can't answer"},
		{"too large", ErrImageTooLarge, "too large"},
		{"invalid type", ErrInvalidImageType, "not supported"},
		{"other", errors.New("boom"), "Failed to answer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := explainErrorToUserText(tc.err); !strings.Contains(got, tc.want) {
				t.Fatalf("explainErrorToUserText(%v) = %q, want substring %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestDownloadTelegramPhoto(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := &mockTelegramServer{filePath: "photos/pic.jpg"}
		b := newMockBot(t, srv)

		image, mimeType, err := downloadTelegramPhoto(context.Background(), b, "file-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(image) == 0 {
			t.Fatal("expected image bytes")
		}
		if mimeType != "image/jpeg" {
			t.Fatalf("expected image/jpeg, got %q", mimeType)
		}
	})

	t.Run("empty file path", func(t *testing.T) {
		srv := &mockTelegramServer{filePath: ""}
		b := newMockBot(t, srv)

		if _, _, err := downloadTelegramPhoto(context.Background(), b, "file-1"); err == nil {
			t.Fatal("expected error for empty file path")
		}
	})

	t.Run("get file error", func(t *testing.T) {
		srv := &mockTelegramServer{filePath: "photos/pic.jpg", failPrefixes: []string{"getFile"}}
		b := newMockBot(t, srv)

		if _, _, err := downloadTelegramPhoto(context.Background(), b, "file-1"); err == nil {
			t.Fatal("expected error for getFile failure")
		}
	})

	t.Run("download http error", func(t *testing.T) {
		srv := &mockTelegramServer{filePath: "photos/pic.jpg", failPrefixes: []string{"/file/"}}
		b := newMockBot(t, srv)

		if _, _, err := downloadTelegramPhoto(context.Background(), b, "file-1"); err == nil {
			t.Fatal("expected error for download failure")
		}
	})
}

func TestTruncateFileID(t *testing.T) {
	if got := truncateFileID("short"); got != "short" {
		t.Fatalf("expected short id unchanged, got %q", got)
	}
	long := strings.Repeat("a", 40)
	got := truncateFileID(long)
	if got != strings.Repeat("a", 16)+"..." {
		t.Fatalf("unexpected truncated id: %q", got)
	}
}

func TestLoadGeminiTimeout(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{"unset uses default", "", false, defaultExplainTimeout},
		{"valid seconds", "30", false, 30 * time.Second},
		{"non-numeric", "abc", true, 0},
		{"zero", "0", true, 0},
		{"negative", "-5", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GEMINI_TIMEOUT_SECONDS", tc.value)
			got, err := loadGeminiTimeout()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInitGeminiExplainer(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		t.Setenv("GEMINI_API_KEY", "")
		if _, err := initGeminiExplainer(); err == nil {
			t.Fatal("expected error for missing key")
		}
	})

	t.Run("invalid timeout", func(t *testing.T) {
		t.Setenv("GEMINI_API_KEY", "test-key")
		t.Setenv("GEMINI_TIMEOUT_SECONDS", "nope")
		if _, err := initGeminiExplainer(); err == nil {
			t.Fatal("expected error for invalid timeout")
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Setenv("GEMINI_API_KEY", "test-key")
		t.Setenv("GEMINI_TIMEOUT_SECONDS", "")
		t.Setenv("GEMINI_MODEL", "")
		explainer, err := initGeminiExplainer()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if explainer == nil || explainer.generator == nil {
			t.Fatal("expected initialized explainer")
		}
		if explainer.model != defaultGeminiModelName {
			t.Fatalf("expected default model, got %q", explainer.model)
		}
	})
}

func TestRun_MissingToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	err := Run()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}

func TestWireOTelTransportsAndTelegramClient(t *testing.T) {
	requireNotPanics(t, wireOTelTransports)
	requireNotPanics(t, wireOTelTransports)

	client := newTelegramHTTPClient()
	if client == nil {
		t.Fatal("expected non-nil telegram client")
	}
	if client.Timeout != telegramPollTimeout {
		t.Fatalf("unexpected timeout: %v", client.Timeout)
	}
}

func TestRecordGeminiTokenUsage(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
		},
	}
	requireNotPanics(t, func() { recordGeminiTokenUsage(context.Background(), "gemini-test", resp) })
	requireNotPanics(t, func() { recordGeminiTokenUsage(context.Background(), "gemini-test", nil) })
	requireNotPanics(t, func() { recordGeminiTokenUsage(context.Background(), "gemini-test", &genai.GenerateContentResponse{}) })
}

func useRedirectedHistClient(t *testing.T, serverURL string) {
	t.Helper()

	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("invalid test server url %q: %v", serverURL, err)
	}
	orig := histHTTPClient
	histHTTPClient = &http.Client{
		Timeout: orig.Timeout,
		Transport: &rewriteHostTransport{
			base:   http.DefaultTransport,
			target: target,
		},
	}
	t.Cleanup(func() { histHTTPClient = orig })
}

func testHistParams() *dbn_hist.SubmitJobParams {
	return &dbn_hist.SubmitJobParams{
		Dataset: "EQUS.MINI",
		Symbols: "AAPL",
		Schema:  dbn.Schema_Ohlcv1D,
		DateRange: dbn_hist.DateRange{
			Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
		},
	}
}

func TestGetHistoricalRangeWithContext(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("raw-dbn-bytes"))
		}))
		server.Start()
		useRedirectedHistClient(t, server.URL)

		body, err := getHistoricalRangeWithContext(context.Background(), "key", testHistParams())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(body) != "raw-dbn-bytes" {
			t.Fatalf("unexpected body: %q", body)
		}
	})

	t.Run("non-200 returns status error", func(t *testing.T) {
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":{"case":"data_end_after_available_end"}}`))
		}))
		server.Start()
		useRedirectedHistClient(t, server.URL)

		_, err := getHistoricalRangeWithContext(context.Background(), "key", testHistParams())
		var statusErr *httpStatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("expected httpStatusError, got %v", err)
		}
		if statusErr.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("unexpected status: %d", statusErr.StatusCode)
		}
	})
}

func TestFetchHistoricalBars_Errors(t *testing.T) {
	t.Run("invalid range", func(t *testing.T) {
		if _, _, err := fetchHistoricalBars(context.Background(), "AAPL", 0); err == nil {
			t.Fatal("expected error for invalid range")
		}
	})

	t.Run("missing api key", func(t *testing.T) {
		t.Setenv("DATABENTO_API_KEY", "")
		_, _, err := fetchHistoricalBars(context.Background(), "AAPL", 7)
		if !errors.Is(err, errDatabentoAPIKeyNotConfigured) {
			t.Fatalf("expected not-configured error, got %v", err)
		}
	})

	t.Run("http error", func(t *testing.T) {
		t.Setenv("DATABENTO_API_KEY", "key")
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		server.Start()
		useRedirectedHistClient(t, server.URL)

		if _, _, err := fetchHistoricalBars(context.Background(), "AAPL", 7); err == nil {
			t.Fatal("expected http error")
		}
	})

	t.Run("retry then parse error", func(t *testing.T) {
		t.Setenv("DATABENTO_API_KEY", "key")
		var calls int
		server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"detail":{"case":"data_end_after_available_end","payload":{"available_end":"2026-03-07T00:00:00Z"}}}`))
				return
			}
			_, _ = w.Write([]byte("not-valid-dbn"))
		}))
		server.Start()
		useRedirectedHistClient(t, server.URL)

		_, _, err := fetchHistoricalBars(context.Background(), "AAPL", 7)
		if err == nil {
			t.Fatal("expected parse error after retry")
		}
		if calls < 2 {
			t.Fatalf("expected retry request, got %d calls", calls)
		}
	})
}

func TestSendOrEditStockResult(t *testing.T) {
	update := &models.Update{Message: &models.Message{ID: 1, Chat: models.Chat{ID: mockChatID}}}

	t.Run("edits loading message", func(t *testing.T) {
		b, srv := newTestBot(t)
		sendOrEditStockResult(context.Background(), b, update, &models.Message{ID: 1}, nil, "result")
		if !strings.Contains(srv.lastMessage, "result") {
			t.Fatalf("expected edit, got %q", srv.lastMessage)
		}
	})

	t.Run("falls back when edit fails", func(t *testing.T) {
		b, srv := newTestBot(t)
		srv.failNextEdit = true
		sendOrEditStockResult(context.Background(), b, update, &models.Message{ID: 1}, nil, "result")
		if !strings.Contains(srv.lastMessage, "result") {
			t.Fatalf("expected fallback, got %q", srv.lastMessage)
		}
	})

	t.Run("sends without loading message", func(t *testing.T) {
		b, srv := newTestBot(t)
		sendOrEditStockResult(context.Background(), b, update, nil, errors.New("no loading"), "result")
		if !strings.Contains(srv.lastMessage, "result") {
			t.Fatalf("expected send, got %q", srv.lastMessage)
		}
	})
}

func TestUpdateStockLoadingState(t *testing.T) {
	update := &models.Update{Message: &models.Message{ID: 1, Chat: models.Chat{ID: mockChatID}}}

	t.Run("no-op without loading message", func(t *testing.T) {
		b, srv := newTestBot(t)
		updateStockLoadingState(context.Background(), b, update, nil, nil, "text")
		if srv.requestCount() != 0 {
			t.Fatalf("expected no API calls, got %d", srv.requestCount())
		}
	})

	t.Run("no-op with loading error", func(t *testing.T) {
		b, srv := newTestBot(t)
		updateStockLoadingState(context.Background(), b, update, &models.Message{ID: 1}, errors.New("x"), "text")
		if srv.requestCount() != 0 {
			t.Fatalf("expected no API calls, got %d", srv.requestCount())
		}
	})

	t.Run("updates loading message", func(t *testing.T) {
		b, srv := newTestBot(t)
		updateStockLoadingState(context.Background(), b, update, &models.Message{ID: 1}, nil, "text")
		if srv.requestCount() == 0 {
			t.Fatal("expected edit call")
		}
	})
}

func TestStockHandler_ParseError(t *testing.T) {
	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	stockHandler(context.Background(), b, messageUpdate("!s"))

	if !strings.Contains(srv.last(), "please provide") {
		t.Fatalf("expected parse error, got %q", srv.last())
	}
}

func TestStockHandler_Blocked(t *testing.T) {
	orig := blockedStocks
	blockedStocks = map[string]string{"TEAM": "nope"}
	t.Cleanup(func() { blockedStocks = orig })

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	stockHandler(context.Background(), b, messageUpdate("!s TEAM"))

	if !strings.Contains(srv.last(), "nope") {
		t.Fatalf("expected blocked message, got %q", srv.last())
	}
}

func TestStockHandler_QuoteSuccess(t *testing.T) {
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "profile") {
			_ = json.NewEncoder(w).Encode(CompanyProfile{Name: testProfileName, Industry: testIndustryTechnology})
			return
		}
		_ = json.NewEncoder(w).Encode(StockQuote{CurrentPrice: 150.25, Change: 2.5, PercentChange: 1.69})
	}))
	server.Start()
	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	stockHandler(context.Background(), b, messageUpdate("!s AAPL"))

	if !strings.Contains(srv.last(), testProfileName) || !strings.Contains(srv.last(), "150.25") {
		t.Fatalf("expected formatted quote, got %q", srv.last())
	}
}

func TestStockHandler_QuoteError(t *testing.T) {
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	server.Start()
	useRedirectedHTTPClient(t, server.URL)
	t.Setenv("FINNHUB_API_KEY", "test-key")

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	stockHandler(context.Background(), b, messageUpdate("!s AAPL"))

	if !strings.Contains(srv.last(), "Failed to fetch stock quote") {
		t.Fatalf("expected quote failure, got %q", srv.last())
	}
}

func TestStockHandler_HistoricalMissingKey(t *testing.T) {
	t.Setenv("DATABENTO_API_KEY", "")

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	stockHandler(context.Background(), b, messageUpdate("!s AAPL 7d"))

	if !strings.Contains(srv.last(), "DATABENTO_API_KEY") {
		t.Fatalf("expected missing-key message, got %q", srv.last())
	}
}

func TestStockHandler_HistoricalFetchError(t *testing.T) {
	t.Setenv("DATABENTO_API_KEY", "key")
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	server.Start()
	useRedirectedHistClient(t, server.URL)

	srv := &mockTelegramServer{}
	b := newMockBot(t, srv)

	stockHandler(context.Background(), b, messageUpdate("!s AAPL 7d"))

	if !strings.Contains(srv.last(), "Failed to fetch 7-day historical data") {
		t.Fatalf("expected historical failure, got %q", srv.last())
	}
}

func TestAllowAnalysisRequest_NilAndNilLimiter(t *testing.T) {
	prev := analysisLimiter
	analysisLimiter = nil
	t.Cleanup(func() { analysisLimiter = prev })

	allowed, _ := allowAnalysisRequest(nil)
	if allowed {
		t.Fatal("expected nil message to be rejected")
	}

	allowed, _ = allowAnalysisRequest(&models.Message{Chat: models.Chat{ID: mockChatID}})
	if !allowed {
		t.Fatal("expected nil limiter to allow")
	}
}
