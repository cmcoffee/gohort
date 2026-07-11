package core

import "testing"

// TestLookupCategory: the volatile-question canaries (the "$1,600 5090" case and
// its siblings) trigger the gate; casual turns don't.
func TestLookupCategory(t *testing.T) {
	fire := map[string]string{
		"what does a 5090 cost?":              "price",
		"how much is an RTX 5090 these days?": "price",
		"what's the latest version of Go?":    "software version",
		"who is the CEO of Acme right now?":   "current office-holder",
		"is the 5090 in stock anywhere?":      "availability",
		"who won the game last night?":        "standings or score",
	}
	for msg, wantCat := range fire {
		cat, ok := LookupCategory(msg)
		if !ok {
			t.Errorf("LookupCategory(%q) should fire", msg)
			continue
		}
		if cat != wantCat {
			t.Errorf("LookupCategory(%q) = %q, want %q", msg, cat, wantCat)
		}
	}
	quiet := []string{
		"tell me a funny story",
		"what's your favorite color?",
		"can you help me write an email?",
		"thanks, that was helpful!",
	}
	for _, msg := range quiet {
		if _, ok := LookupCategory(msg); ok {
			t.Errorf("LookupCategory(%q) should NOT fire on a casual turn", msg)
		}
	}
}

// TestHasLookupTool: the gate only fires when the agent can actually look up.
func TestHasLookupTool(t *testing.T) {
	withSearch := []AgentToolDef{{Tool: Tool{Name: "store_fact"}}, {Tool: Tool{Name: "web_search"}}}
	if !HasLookupTool(withSearch) {
		t.Error("web_search should register as a lookup tool")
	}
	noSearch := []AgentToolDef{{Tool: Tool{Name: "store_fact"}}, {Tool: Tool{Name: "link_entities"}}}
	if HasLookupTool(noSearch) {
		t.Error("no search/fetch/browse tool present, should be false")
	}
	if HasLookupTool(nil) {
		t.Error("nil tools should be false")
	}
}

// TestLatestUserContent: returns the most recent user turn.
func TestLatestUserContent(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
	}
	if got := LatestUserContent(msgs); got != "second" {
		t.Errorf("expected latest user content 'second', got %q", got)
	}
	if got := LatestUserContent([]Message{{Role: "assistant", Content: "x"}}); got != "" {
		t.Errorf("no user message should yield empty, got %q", got)
	}
}
