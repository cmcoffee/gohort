package extensions

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Describe a change to a skill: the worker is handed the skill and the change,
// what it returns replaces the four fields the form owns, the rest of the
// skill is untouched, and the version before is in the history.
func TestSkillReviseFromADescription(t *testing.T) {
	app, req := authedExtensions(t)
	made, err := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "refunds", Description: "handles refunds",
		Triggers: []string{"refund"}, Instructions: "Check the order date.", AllowedTools: []string{"lookup_order"}})
	if err != nil {
		t.Fatal(err)
	}
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"name":"refunds","description":"handles refunds",` +
		`"triggers":["refund","money back"],"instructions":"Ask for the order number first. Check the order date."}`}}}
	app.LLM = fake

	r := req("POST", "/api/skills/"+made.ID+"/revise")
	r.Body = readCloser(`{"description":"ask for the order number first, and also catch money back"}`)
	w := httptest.NewRecorder()
	serve(app)(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "instructions") || !strings.Contains(w.Body.String(), "triggers") {
		t.Errorf("the reply does not say what changed: %s", w.Body.String())
	}
	got, _ := findOwnSkill("alice", made.ID)
	if !strings.HasPrefix(got.Instructions, "Ask for the order number first.") || len(got.Triggers) != 2 {
		t.Fatalf("not revised: %+v", got.SkillRecord)
	}
	if len(got.AllowedTools) != 1 || got.AllowedTools[0] != "lookup_order" {
		t.Errorf("a field the change does not own was touched: %+v", got.AllowedTools)
	}

	w = httptest.NewRecorder()
	serve(app)(w, req("GET", "/api/skills/"+made.ID+"/revisions"))
	if !strings.Contains(w.Body.String(), "revised from a description") {
		t.Errorf("the version before is not in the history: %s", w.Body.String())
	}
}

// A reply that is not a usable skill is retried once, then refused, and the
// skill is left alone.
func TestSkillReviseRefusesAnUnusableReply(t *testing.T) {
	app, req := authedExtensions(t)
	made, _ := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "refunds", Instructions: "Check the order date."})
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: "sure, here you go"}, {Content: `{"name":""}`}}}
	r := req("POST", "/api/skills/"+made.ID+"/revise")
	r.Body = readCloser(`{"description":"make it friendlier"}`)
	w := httptest.NewRecorder()
	serve(app)(w, r)
	if w.Code == 200 {
		t.Fatalf("an unusable reply was accepted: %s", w.Body.String())
	}
	got, _ := findOwnSkill("alice", made.ID)
	if got.Instructions != "Check the order date." {
		t.Errorf("the skill changed after a refused revision: %q", got.Instructions)
	}
}

func readCloser(s string) *nopCloser { return &nopCloser{strings.NewReader(s)} }

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }
