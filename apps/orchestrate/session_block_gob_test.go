package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A session that ends on an ask_user_form saves and reads back with the form,
// the way a reload redraws it. It failed to save at all once: the form's
// questions are a []map[string]any inside Block, which gob refused.
func TestASessionEndingOnAFormSaves(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	block := map[string]any{
		"kind": "block", "type": "ui_ask_form", "id": "askform-1",
		"steps": []map[string]any{
			{"question": "Which city?", "type": "text"},
			{"question": "What time?", "options": []string{"7:00 AM", "8:00 AM"}},
		},
	}
	sess := ChatSession{ID: "s1", AgentID: "a1", Messages: []ChatMessage{
		{Role: "user", Content: "a weather check"},
		{Role: "assistant", Content: "(form with 2 questions)", Block: block},
	}}
	if _, err := saveChatSession(db, sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadChatSession(db, "a1", "s1")
	if !ok || len(got.Messages) != 2 {
		t.Fatalf("the session did not come back: ok=%v %+v", ok, got)
	}
	steps, _ := got.Messages[1].Block["steps"].([]map[string]any)
	if len(steps) != 2 || steps[0]["question"] != "Which city?" {
		t.Errorf("the form did not come back: %#v", got.Messages[1].Block["steps"])
	}
}

// An ask card's answer keeps the card's state on the message, so a reload
// fills the card in instead of drawing the answer as a bubble. It is stored
// as the JSON the page sent: decoded, its nested values are what gob refused
// in the form above.
func TestACardAnswerSavesItsState(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	state := askStateOf([]byte(` {"values":[{"picks":["7:00 AM"],"other":""},"Lisbon"]} `))
	if state == nil {
		t.Fatal("a valid state was dropped")
	}
	sess := ChatSession{ID: "s1", AgentID: "a1", Messages: []ChatMessage{
		{Role: "assistant", Content: "(form with 2 questions)", Block: map[string]any{"type": "ui_ask_form"}},
		{Role: "user", Content: "1. What time? -> 7:00 AM\n2. Which city? -> Lisbon", AskState: state},
	}}
	if _, err := saveChatSession(db, sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadChatSession(db, "a1", "s1")
	if !ok || len(got.Messages) != 2 || string(got.Messages[1].AskState) != `{"values":[{"picks":["7:00 AM"],"other":""},"Lisbon"]}` {
		t.Fatalf("the answer's state did not come back: %+v", got.Messages)
	}
}

// What a send may store as card state: one JSON object, small.
func TestAskStateIsAnObjectWithinItsCap(t *testing.T) {
	for _, bad := range []string{"", "null", `"text"`, `[1,2]`, `{"picks":`, `{"other":"` + strings.Repeat("x", maxAskStateBytes) + `"}`} {
		if askStateOf([]byte(bad)) != nil {
			t.Errorf("stored %.40q", bad)
		}
	}
	if askStateOf([]byte(`{"picks":["A"],"other":""}`)) == nil {
		t.Error("a plain state was dropped")
	}
}
