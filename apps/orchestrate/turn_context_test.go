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
