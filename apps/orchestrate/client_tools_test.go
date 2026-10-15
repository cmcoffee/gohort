package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The desktop app's tools come and go with its connection: they go last in
// the catalog and are named on the turn, so the roster in the system prompt
// and every definition before them are the same whether or not it is there.
func TestDesktopToolsStayOutOfTheStablePrompt(t *testing.T) {
	td := func(n string) AgentToolDef { return AgentToolDef{Tool: Tool{Name: n}} }
	with := []AgentToolDef{td("recall"), td(ClientToolPrefix + "read_file"), td("schedule"), td(ClientToolPrefix + "contacts_search"), td("web_search")}
	without := []AgentToolDef{td("recall"), td("schedule"), td("web_search")}
	got := clientToolsLast(with)
	var names []string
	for _, x := range got {
		names = append(names, x.Tool.Name)
	}
	if strings.Join(names[:3], ",") != "recall,schedule,web_search" || !strings.HasPrefix(names[3], ClientToolPrefix) {
		t.Errorf("order: %v", names)
	}
	s1, c1 := splitClientTools(with)
	s2, _ := splitClientTools(without)
	if buildToolUseDirective(s1) != buildToolUseDirective(s2) {
		t.Error("the roster differs with the desktop app connected")
	}
	if note := clientToolsNote(c1); !strings.Contains(note, ClientToolPrefix+"read_file") || !strings.HasPrefix(note, frameworkNoteTag) {
		t.Errorf("note: %q", note)
	}
	if clientToolsNote(nil) != "" {
		t.Error("a note with no desktop tools")
	}
}

// An agent's saved facts stay in the SYSTEM prompt, and the note added to the
// person's message stays short. Moving facts onto the user turn (0.8.287) made
// the hybrid worker model (Qwen 3.8 on vLLM) start almost every conversation
// cold: vLLM restarts such a model only from the last 1600-token boundary of
// a previous prompt, so a prompt ending ~1,200 tokens of notes past the
// shared part leaves that restart point where no other conversation can use
// it. Measured: 5.2s instead of 0.5s before the first token. If this test
// fails because you moved them, measure that first (see memory.go,
// prependAgentContext).
func TestSavedFactsStayInTheSystemPrompt(t *testing.T) {
	const turnNoteWarnChars = 2000 // core's warning threshold (agent_loop_config.go)
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	rec, err := saveAgent(udb, AgentRecord{Name: "Helper", Owner: user, OrchestratorPrompt: "You help."})
	if err != nil {
		t.Fatal(err)
	}
	StoreMemoryFact(udb, factsNamespace(rec.ID), "The user's dog is named Pickle.")
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Hi."}}}
	T.LLM = fake
	agent, _ := loadAgent(udb, rec.ID)
	body := strings.NewReader(`{"message":"Hello"}`)
	T.handleSend(httptest.NewRecorder(), asUser(httptest.NewRequest(http.MethodPost, "/api/send?agent_id="+rec.ID, body), user), udb, user, agent)
	if fake.Calls() == 0 {
		t.Fatal("no model call")
	}
	sys := strings.Join(systemPromptsSeen(fake)[:1], "")
	last := ""
	for _, m := range fake.Sent(0) {
		if m.Role == "user" {
			last = m.Content
		}
	}
	if !strings.Contains(sys, "Pickle") {
		t.Error("a saved fact left the system prompt: on the hybrid worker model that makes new conversations start cold (see the comment above)")
	}
	if strings.Contains(last, "Pickle") {
		t.Error("a saved fact rides the user turn: on the hybrid worker model that makes new conversations start cold (see the comment above)")
	}
	if n := len(last); n > turnNoteWarnChars {
		t.Errorf("the user turn is %d chars on a plain hello; the per-turn tail must stay short (under %d)", n, turnNoteWarnChars)
	}
}
