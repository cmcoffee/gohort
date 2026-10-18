package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/silent"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A turn the model closed with stay_silent is saved as having stayed quiet,
// and why. It was saved as "This reply didn't finish, the turn finished
// without producing a reply", which says the turn failed.
func TestAQuietTurnIsSavedAsQuietWithItsReason(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	sess := ChatSession{ID: "s1", AgentID: "wren", Messages: []ChatMessage{{Role: "user", Content: "ok thanks"}}}
	turn := &chatTurn{turnClosed: true, silenceReason: "they were only saying thanks"}
	turn.recordToolCall(toolCallRecord{Name: "stay_silent", Args: map[string]any{"reason": "they were only saying thanks"}, Result: "Silence acknowledged."})
	persistQuietTurn(&sess, udb, turn)

	got, ok := loadChatSession(udb, "wren", "s1")
	if !ok || len(got.Messages) != 2 {
		t.Fatalf("the quiet turn should be saved after the request: %+v", got.Messages)
	}
	last := got.Messages[1]
	if last.Role != "assistant" || last.Content != "" {
		t.Errorf("a quiet turn has no reply text: %+v", last)
	}
	if last.Label != "Stayed quiet: they were only saying thanks" {
		t.Errorf("label = %q", last.Label)
	}
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].Label != last.Label {
		t.Errorf("the silence should be labelled in the tool runs too: %+v", last.ToolCalls)
	}
	if strings.Contains(last.Content+last.Label, "didn't finish") {
		t.Error("a quiet turn is not an unfinished one")
	}
	if quietLabel("  ") != "Stayed quiet (no reason given)" {
		t.Errorf("no reason = %q", quietLabel(""))
	}
}

// stay_silent records the reason it was given, for that label.
func TestStaySilentKeepsItsReason(t *testing.T) {
	sess := &ToolSession{}
	if _, err := new(silent.StaySilentTool).RunWithSession(map[string]any{"reason": " the picture is the reply "}, sess); err != nil {
		t.Fatal(err)
	}
	if !sess.Silenced || sess.SilencedReason != "the picture is the reply" {
		t.Errorf("silenced=%v reason=%q", sess.Silenced, sess.SilencedReason)
	}
}
