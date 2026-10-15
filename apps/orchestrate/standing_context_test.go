package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// What changes with an agent's own activity (a fact it saved) rides the newest
// user message, so the system prompt, and the tool definitions after it, stay
// byte-identical from one conversation to the next and the model's prompt
// cache serves them. A fact in the system prompt cost a cold prefill of the
// whole prompt each time one changed.
func TestAFactSavedBetweenConversationsLeavesTheSystemPromptAlone(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	rec, err := saveAgent(udb, AgentRecord{Name: "Helper", Owner: user, OrchestratorPrompt: "You help."})
	if err != nil {
		t.Fatal(err)
	}
	StoreMemoryFact(udb, factsNamespace(rec.ID), "The user's dog is named Pickle.")

	send := func() (string, string) {
		fake := &FakeLLM{Turns: []FakeTurn{{Content: "Hi."}}}
		T.LLM = fake
		agent, _ := loadAgent(udb, rec.ID)
		body, _ := json.Marshal(map[string]any{"message": "Hello"})
		w := httptest.NewRecorder()
		T.handleSend(w, asUser(httptest.NewRequest(http.MethodPost, "/api/send?agent_id="+rec.ID, strings.NewReader(string(body))), user), udb, user, agent)
		if fake.Calls() == 0 {
			t.Fatalf("no model call: %s", w.Body.String())
		}
		sys := strings.Join(systemPromptsSeen(fake)[:1], "")
		last := ""
		for _, m := range fake.Sent(0) {
			if m.Role == "user" {
				last = m.Content
			}
		}
		return sys, last
	}
	sys1, user1 := send()
	if strings.Contains(sys1, "Pickle") {
		t.Error("a saved fact is in the system prompt")
	}
	if !strings.Contains(user1, "Pickle") || !strings.Contains(user1, "not written by the user") {
		t.Errorf("the fact is not on the user turn, framed as reference: %q", user1)
	}

	StoreMemoryFact(udb, factsNamespace(rec.ID), "The user prefers tea.")
	sys2, user2 := send()
	if sys1 != sys2 {
		t.Errorf("the system prompt changed when a fact was saved (%d vs %d bytes)", len(sys1), len(sys2))
	}
	if !strings.Contains(user2, "tea") {
		t.Errorf("the new fact did not reach the turn: %q", user2)
	}
}

// A dispatched run (a schedule, a channel) builds its prompt the same way.
func TestADispatchedRunCarriesItsFactsOnTheTurn(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	target := AgentRecord{ID: "ag-x", Name: "X", Owner: "u", OrchestratorPrompt: "persona"}
	facts := []MemoryFact{{Note: "Remember the blue door."}}
	sys, standing := dispatchSystemPrompt(t.Context(), target, facts, "", "", "chan:1", udb, "u")
	if strings.Contains(sys, "blue door") || !strings.Contains(standing, "blue door") {
		t.Errorf("sys has fact=%v, standing has fact=%v", strings.Contains(sys, "blue door"), strings.Contains(standing, "blue door"))
	}
	notes := withStandingNotes(standing, func(string) string { return "other note" })("hi")
	if !strings.HasPrefix(notes, frameworkNoteTag) || !strings.Contains(notes, "blue door") || !strings.HasSuffix(notes, "other note") {
		t.Errorf("turn notes: %q", notes)
	}
}

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
