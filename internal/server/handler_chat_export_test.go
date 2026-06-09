package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

func TestHandleChatJSONLReturnsActiveRawContent(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-jsonl-active"
	seedSearchableChat(t, fixture.chats, chatID)
	want, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load raw jsonl: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/jsonl?chatId="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content-type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `inline; filename="chat-jsonl-active.jsonl"` {
		t.Fatalf("content-disposition=%q", got)
	}
	if rec.Body.String() != want {
		t.Fatalf("raw jsonl mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatJSONLFallsBackToArchiveRawContent(t *testing.T) {
	server, active, _ := newArchiveHandlerTestServer(t, nil)
	chatID := "chat-jsonl-archive"
	seedArchiveHandlerChat(t, active, chatID)
	want, err := active.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load active raw jsonl: %v", err)
	}

	archiveRec := httptest.NewRecorder()
	server.ServeHTTP(archiveRec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", strings.NewReader(`{"chatIds":["`+chatID+`"]}`)))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/jsonl?chatId="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != want {
		t.Fatalf("archived raw jsonl mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatJSONLValidationAndNotFound(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		name string
		path string
		code int
	}{
		{name: "missing", path: "/api/chat/jsonl", code: http.StatusBadRequest},
		{name: "invalid", path: "/api/chat/jsonl?chatId=../chat", code: http.StatusBadRequest},
		{name: "not found", path: "/api/chat/jsonl?chatId=missing-chat", code: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestLoadJSONLContentRejectsInvalidChatID(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.LoadJSONLContent("../chat"); err == nil {
		t.Fatalf("expected invalid chatId error")
	}
}

func TestRenderChatMarkdownSkipsAutomationQuery(t *testing.T) {
	markdown := renderChatMarkdown("Automation", "agent-a", []stream.EventData{
		{
			Type:      "request.query",
			Timestamp: 100,
			Payload: map[string]any{
				"message": "Secret automation prompt",
				"role":    "automation",
			},
		},
		{
			Type:      "content.snapshot",
			Timestamp: 200,
			Payload: map[string]any{
				"text": "Automation result",
			},
		},
	})

	if strings.Contains(markdown, "Secret automation prompt") || strings.Contains(markdown, "## User") {
		t.Fatalf("expected automation query to be omitted, got:\n%s", markdown)
	}
	if !strings.Contains(markdown, "Automation result") {
		t.Fatalf("expected assistant content to remain, got:\n%s", markdown)
	}
}
