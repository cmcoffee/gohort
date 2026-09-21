package agents

// The standing line on an agent that is not yours.
//
// It replaces every per-turn message about a guardrail. One that appears only
// when a rule fires IS the rule firing, so the useful part — a refusal here may
// be deliberate, and somebody else can change it — is said on every turn
// instead, where it distinguishes none of them and arrives before the wall
// rather than after it.

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	"github.com/cmcoffee/gohort/core/ui"
)

func TestYourOwnAgentSaysNothing(t *testing.T) {
	if got := ownerConfigNote(orchestrate.AgentRecord{ID: "a1", Owner: "alice"}, "alice"); got != "" {
		t.Errorf("your own agent explained itself to you: %q", got)
	}
	if got := ownerConfigNote(orchestrate.AgentRecord{ID: "a1"}, "alice"); got != "" {
		t.Errorf("an agent with no owner recorded has nobody to name: %q", got)
	}
}

// Named on a peer share, not on a published one: an account here is an email
// address, and a published agent reaches everybody signed in.
func TestTheNoteNamesTheOwnerOnlyOnAPeerShare(t *testing.T) {
	shared := orchestrate.AgentRecord{ID: "a1", Owner: "alice", AllowedUsers: []string{"bob"}}
	note := ownerConfigNote(shared, "bob")
	if !strings.Contains(note, "alice") {
		t.Errorf("a peer share should name who to ask: %q", note)
	}
	published := orchestrate.AgentRecord{ID: "a2", Owner: "alice", Everyone: true}
	if got := ownerConfigNote(published, "dana"); strings.Contains(got, "alice") {
		t.Errorf("a published agent handed a stranger the owner's account: %q", got)
	}
	// Either way it says the two useful things: somebody else configures this,
	// and there is a way to ask them.
	for _, n := range []string{note, ownerConfigNote(published, "dana")} {
		if !strings.Contains(n, "configuration") || !strings.Contains(n, "Ask the owner") {
			t.Errorf("the note does not say what it is for: %q", n)
		}
	}
}

// It sits ABOVE the conversation, and only when there is one to show.
func TestTheNoteSitsAboveTheConversation(t *testing.T) {
	panel := ui.AgentLoopPanel{SendURL: "api/send"}
	plain := chatSections("", panel)
	if len(plain) != 1 {
		t.Fatalf("an agent of your own should render the chat alone: %d sections", len(plain))
	}
	withNote := chatSections("this is somebody else's", panel)
	if len(withNote) != 2 {
		t.Fatalf("expected the note and the chat: %d sections", len(withNote))
	}
	if withNote[0].Subtitle != "this is somebody else's" {
		t.Errorf("the note is not the first thing on the page: %+v", withNote[0])
	}
	if withNote[1].Body == nil {
		t.Error("the conversation went missing under the note")
	}
}
