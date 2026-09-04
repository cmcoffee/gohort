package orchestrate

import (
	"strings"
	"testing"
)

// The live failure, 2026-09-03: an agent three messages into a session with no
// recall behind it called recall(id:"2bd98b45-…") — a UUID nobody gave it, for
// "that research" that did not exist. The old error described the FORMAT, so
// the model corrected the format and re-sent the same invented reference twice.
func TestAnInventedRecallIDIsNamedAsInvented(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "tell me a story"},
		{Role: "assistant", Content: "I'd love to. What genre?"},
		{Role: "user", Content: "science fiction"},
	}}}

	_, err := turn.recallFetch("2bd98b45-04b0-4fc4-8de4-df71f9a570e8")
	if err == nil {
		t.Fatal("an id nobody issued must not resolve")
	}
	// The instruction that ends the loop is the name of the tool that produces
	// real ids, not a restatement of the id grammar.
	if !strings.Contains(err.Error(), "query") {
		t.Errorf("the error must send the model to a query: %v", err)
	}
	if !strings.Contains(err.Error(), "never constructed") {
		t.Errorf("the error must say ids are not constructed: %v", err)
	}

	// The second and third calls of the live transcript: same fabrication, one
	// with a prefix that was not even in the list the model had just been shown,
	// one with a real prefix. Both must land on the same answer.
	for _, id := range []string{
		"knowledge:2bd98b45-04b0-4fc4-8de4-df71f9a570e8",
		"doc:2bd98b45-04b0-4fc4-8de4-df71f9a570e8",
	} {
		_, err := turn.recallFetch(id)
		if err == nil || !strings.Contains(err.Error(), "never constructed") {
			t.Errorf("%s: a better-formed fabrication is still a fabrication, got %v", id, err)
		}
	}
}

// An id the agent was actually handed is not a fabrication, however it arrived.
// A false negative here would refuse a legitimate lookup, so the scan is
// deliberately generous: the bare reference counts, and a user typing an id
// counts.
func TestAnIssuedRecallIDIsNotTreatedAsInvented(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "what do we know about drones?"},
		{Role: "assistant", ToolCalls: []PersistedToolCall{{
			Name:   "recall",
			Result: "- [knowledge] Autonomous systems\n  id: doc:abc123\n  Some excerpt.",
		}}},
	}}}

	if !turn.recallIDWasIssued("doc:abc123") {
		t.Error("an id printed in a recall result was issued")
	}
	// Re-prefixing a real reference is malformed, not invented — the agent has
	// the ref, it just dressed it wrong.
	if !turn.recallIDWasIssued("abc123") {
		t.Error("the bare reference counts as issued")
	}
	if !turn.recallIDWasIssued("wrongkind:abc123") {
		t.Error("a real ref under a wrong prefix is malformed, not invented")
	}
	// So that one gets the FORMAT error, which is the correct advice for it.
	_, err := turn.recallFetch("wrongkind:abc123")
	if err == nil || !strings.Contains(err.Error(), "unrecognized id") {
		t.Errorf("a malformed issued id should get the format error, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "never constructed") {
		t.Error("an id the agent really holds must not be called a fabrication")
	}

	// An id the user typed is issued too.
	typed := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "open doc:xyz789 for me"},
	}}}
	if !typed.recallIDWasIssued("doc:xyz789") {
		t.Error("an id the user typed was issued")
	}
}

// forget deletes, and its miss message ("it may already be gone") reads as
// confirmation that the thing existed and is now handled — the worst possible
// answer to an id nobody ever issued.
func TestForgetRefusesAnInventedID(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "forget what you know about me"},
	}}}
	if turn.recallIDWasIssued("fact:made-up-id") {
		t.Fatal("nothing in this session issued that id")
	}
	err := turn.inventedRecallIDError("fact:made-up-id")
	if !strings.Contains(err.Error(), "query") || !strings.Contains(err.Error(), "never constructed") {
		t.Errorf("forget must give the same directive as recall: %v", err)
	}
}

// No session, nothing issued — and the guard must not panic reaching for one.
func TestRecallIDIssuedIsSafeWithoutASession(t *testing.T) {
	if (&chatTurn{}).recallIDWasIssued("doc:abc") {
		t.Error("no session, no issued ids")
	}
	if (&chatTurn{session: &ChatSession{}}).recallIDWasIssued("") {
		t.Error("an empty id was never issued")
	}
}

// The tool description has to carry the rule too: the error is the backstop,
// the description is what stops the call being made at all.
func TestRecallDescriptionForbidsConstructingIDs(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{Name: "Wren"}}
	def := turn.recallToolDef()
	if !strings.Contains(def.Tool.Description, "never construct") {
		t.Error("the recall description must forbid constructing ids")
	}
	idParam, ok := def.Tool.Parameters["id"]
	if !ok {
		t.Fatal("recall must still take an id")
	}
	if !strings.Contains(idParam.Description, "COPIED") {
		t.Errorf("the id parameter must say where an id comes from: %q", idParam.Description)
	}
}
