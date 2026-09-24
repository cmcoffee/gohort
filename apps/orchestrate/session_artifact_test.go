package orchestrate

// A conversation travels as a file and opens read-only: a turn on it is
// refused, and Continue starts a new one that carries it as quoted text, never
// as the agent's own replies or tool results.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnImportedConversationIsReadOnlyAndContinuesAsQuotedText(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	RegisterAgentArtifactType(T)
	RegisterSessionArtifactType(T)

	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Helper", OrchestratorPrompt: "help"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := saveChatSession(udb, ChatSession{
		AgentID: a.ID, Title: "Trip planning", Created: time.Now(),
		Messages: []ChatMessage{
			{Role: "user", Content: "book the train"},
			{Role: "assistant", Content: "Done.", ToolCalls: []PersistedToolCall{{Name: "book", Result: "confirmation 42"}},
				Mark: &MessageMark{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sa := &sessionArtifact{app: T}
	raw, err := sa.ExportArtifact(nil, a.ID+"/"+sess.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sess.ID) {
		t.Error("the recipe carries the session id")
	}

	// Into another account that has an agent of the same name.
	AuthDB().Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	budb := UserDB(T.DB, "bob")
	ba, _ := saveAgent(budb, AgentRecord{Owner: "bob", Name: "Helper", OrchestratorPrompt: "help"})
	if _, skip, err := sa.ImportArtifact(nil, raw, "bob"); err != nil || skip != "" {
		t.Fatalf("import: %q %v", skip, err)
	}
	var imported ChatSession
	for _, s := range listChatSessions(budb, ba.ID) {
		if s.Title == "Trip planning" {
			imported, _ = loadChatSession(budb, ba.ID, s.ID)
		}
	}
	if imported.ID == "" || imported.Imported == nil {
		t.Fatalf("the session did not land marked imported: %+v", imported)
	}
	if imported.Messages[1].Mark != nil {
		t.Error("a per-message mark travelled")
	}

	// A turn on it is refused before anything runs.
	body, _ := json.Marshal(map[string]any{"message": "and the hotel?", "session_id": imported.ID})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/send?agent_id="+ba.ID, strings.NewReader(string(body)))
	T.handleSend(w, r, budb, "bob", ba)
	if !strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("a turn ran on an imported session: %s", w.Body.String())
	}

	// Continue: a new session, the transcript as ONE fenced user message.
	w = httptest.NewRecorder()
	T.handleSessionContinue(w, httptest.NewRequest(http.MethodPost, "/", nil), budb, ba, imported.ID)
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	next, ok := loadChatSession(budb, ba.ID, out["id"])
	if !ok || next.Imported != nil {
		t.Fatalf("continue: %d %s", w.Code, w.Body.String())
	}
	if len(next.Messages) != 2 || next.Messages[0].Role != "user" || !strings.Contains(next.Messages[0].Content, "IMPORTED-TRANSCRIPT-") ||
		!strings.Contains(next.Messages[0].Content, "confirmation 42") {
		t.Fatalf("the transcript should ride one quoted user message: %+v", next.Messages)
	}
	for _, m := range next.Messages {
		if len(m.ToolCalls) > 0 {
			t.Error("a continued session replays tool calls")
		}
	}
	// And Continue on an ordinary session is refused.
	w = httptest.NewRecorder()
	T.handleSessionContinue(w, httptest.NewRequest(http.MethodPost, "/", nil), budb, ba, next.ID)
	if w.Code != http.StatusBadRequest {
		t.Errorf("continue on an ordinary session: %d", w.Code)
	}
}
