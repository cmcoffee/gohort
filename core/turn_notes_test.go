// TurnNotes: volatile, turn-scoped context appended to the newest user message.
//
// The seam exists because a tool SCHEMA cannot carry anything that changes
// between turns — schemas sit at the front of the prompt, so a changing one
// re-pays cold prefill every turn. The newest user turn has the opposite
// property: it never hits cache anyway, which is why the date stamp lives there.
package core

import (
	"strings"
	"testing"
)

func TestATurnNoteLandsOnTheNewestUserMessage(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "older question"},
		{Role: "assistant", Content: "older answer"},
		{Role: "user", Content: "add Alex to the garage picture"},
	}
	cfg := AgentLoopConfig{TurnNotes: func(user string) string {
		// The app is handed the user's OWN words — not a message that opens
		// with a timestamp it has to look past to understand the request.
		if strings.Contains(user, "[Current date & time:") {
			t.Errorf("TurnNotes saw a stamped message: %q", user)
		}
		return "recent images: image#1 — the garage"
	}}

	applyTurnNotes(cfg, history)

	if !strings.Contains(history[2].Content, "image#1 — the garage") {
		t.Errorf("note missing from the newest user turn: %q", history[2].Content)
	}
	if !strings.Contains(history[2].Content, "add Alex to the garage picture") {
		t.Errorf("the user's own words must survive: %q", history[2].Content)
	}
	// Earlier turns are settled context and part of the cached prefix. Writing
	// into one moves the cache boundary backwards for no gain.
	if strings.Contains(history[0].Content, "image#1") {
		t.Error("an earlier user turn must not be touched")
	}
}

func TestATurnNoteIsSkippedWhenThereIsNothingToSay(t *testing.T) {
	history := []Message{{Role: "user", Content: "what's for dinner"}}
	applyTurnNotes(AgentLoopConfig{TurnNotes: func(string) string { return "   " }}, history)
	if history[0].Content != "what's for dinner" {
		t.Errorf("a blank note must add nothing, got %q", history[0].Content)
	}
	// No hook at all is the shape every host had before this existed.
	applyTurnNotes(AgentLoopConfig{}, history)
	if history[0].Content != "what's for dinner" {
		t.Errorf("no hook must add nothing, got %q", history[0].Content)
	}
}

func TestATurnNoteNeverLandsOnAToolResult(t *testing.T) {
	// Mid-loop the trailing message is a tool result, not the human turn.
	// Appending there would put reference material inside a result the model
	// reads as output from something it just ran.
	history := []Message{
		{Role: "user", Content: "blend these"},
		{Role: "assistant", Content: ""},
		{Role: "tool", Content: "edit failed"},
	}
	applyTurnNotes(AgentLoopConfig{TurnNotes: func(string) string { return "NOTE" }}, history)
	for _, m := range history {
		if strings.Contains(m.Content, "NOTE") {
			t.Errorf("note landed on a %s message: %q", m.Role, m.Content)
		}
	}
	applyTurnNotes(AgentLoopConfig{TurnNotes: func(string) string { return "NOTE" }}, nil) // must not panic
}
