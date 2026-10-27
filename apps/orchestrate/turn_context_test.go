package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appagents"
)

// What a host app puts on a send with agenthost.WithTurnContext reaches the
// model on the newest user message, and is not kept: the stored conversation
// holds what the person typed, so the next turn does not carry this one's
// copy of the editor.
func TestATurnContextReachesTheModelAndIsNotStored(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Done."}}}
	T.LLM = fake
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-turn-context", OwningApp: "Test", Name: "Editor", Prompt: "You help with the open file.",
		AllowedTools: []string{"ask_user"}, Hidden: true, ExactTools: true,
	})
	agent, ok := loadAgent(udb, "app-test-turn-context")
	if !ok {
		t.Fatal("the app agent did not resolve")
	}
	const editor = "The editor holds main.go:\nfunc main() {}"
	body, _ := json.Marshal(map[string]any{"message": "Add a comment."})
	r := asUser(httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(string(body))), user)
	w := httptest.NewRecorder()
	T.handleSendWithAppTools(w, agenthost.WithTurnContext(r, editor), udb, user, agent, []AgentToolDef{{
		Tool:    Tool{Name: "noop", Description: "Does nothing."},
		Handler: func(context.Context, map[string]any) (string, error) { return "", nil },
	}})
	if fake.Calls() == 0 {
		t.Fatalf("the turn never reached the model: %s", w.Body.String())
	}
	sent := fake.Sent(0)
	last := sent[len(sent)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Add a comment.") || !strings.Contains(last.Content, editor) {
		t.Fatalf("the newest user message the model read was %q", last.Content)
	}
	if strings.Contains(fake.Config(0).SystemPrompt, "func main") {
		t.Error("the turn context went into the system prompt")
	}
	sessions := listChatSessions(udb, agent.ID)
	if len(sessions) != 1 {
		t.Fatalf("%d sessions saved", len(sessions))
	}
	s, _ := loadChatSession(udb, agent.ID, sessions[0].ID)
	for _, m := range s.Messages {
		if strings.Contains(m.Content, "func main") {
			t.Errorf("the stored conversation kept the turn context: %s %q", m.Role, m.Content)
		}
	}
}

// A send the page will read aloud (a dictated message with read-aloud on)
// tells the turn to answer the way it would speak; any other send does not,
// and the stored conversation holds what was said either way.
func TestASpokenReplySendIsToldToSpeak(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Noon works.", Repeat: true}}}
	T.LLM = fake
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-spoken-reply", OwningApp: "Test", Name: "Helper", Prompt: "You help.",
		AllowedTools: []string{"ask_user"}, Hidden: true, ExactTools: true,
	})
	agent, ok := loadAgent(udb, "app-test-spoken-reply")
	if !ok {
		t.Fatal("the app agent did not resolve")
	}
	send := func(body map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := asUser(httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(string(raw))), user)
		before := fake.Calls()
		T.handleSendWithAppTools(httptest.NewRecorder(), r, udb, user, agent, nil)
		if fake.Calls() == before {
			t.Fatal("the turn never reached the model")
		}
		sent := fake.Sent(before)
		return sent[len(sent)-1].Content
	}
	if got := send(map[string]any{"message": "When can we meet?", "spoken_reply": true}); !strings.Contains(got, spokenReplyNote) {
		t.Errorf("a spoken-reply send should be told to speak: %q", got)
	}
	if got := send(map[string]any{"message": "When can we meet?"}); strings.Contains(got, "will be heard") {
		t.Errorf("a typed send should not be: %q", got)
	}
	for _, s := range listChatSessions(udb, agent.ID) {
		full, _ := loadChatSession(udb, agent.ID, s.ID)
		for _, m := range full.Messages {
			if strings.Contains(m.Content, "will be heard") {
				t.Errorf("the stored conversation kept the note: %s %q", m.Role, m.Content)
			}
		}
	}
}
