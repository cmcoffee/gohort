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

// What the reviser refuses before it asks anyone: no change described, a body
// that is not JSON, a skill that is not the caller's, a GET, and a change that
// would rename it onto another skill of theirs.
func TestSkillReviseRefusals(t *testing.T) {
	app, req := authedExtensions(t)
	made, _ := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "refunds", Instructions: "Check the order date."})
	SaveSkill(AuthDB(), "alice", SkillRecord{Name: "returns", Instructions: "Ask why."})
	theirs, _ := SaveSkill(AuthDB(), "bob", SkillRecord{Name: "bobs", Instructions: "x"})
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: `{"name":"returns","description":"","triggers":[],"instructions":"Check."}`}}}
	post := func(method, id, body string) int {
		r := req(method, "/api/skills/"+id+"/revise")
		r.Body = readCloser(body)
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code
	}
	for _, c := range []struct {
		what, method, id, body string
		want                   int
	}{
		{"no change described", "POST", made.ID, `{"description":"  "}`, 400},
		{"a body that is not JSON", "POST", made.ID, `{"description":`, 400},
		{"somebody else's skill", "POST", theirs.ID, `{"description":"x"}`, 404},
		{"no such skill", "POST", "nope", `{"description":"x"}`, 404},
		{"a GET", "GET", made.ID, ``, 405},
		{"a rename onto another of theirs", "POST", made.ID, `{"description":"call it returns"}`, 409},
	} {
		if got := post(c.method, c.id, c.body); got != c.want {
			t.Errorf("%s: %d, want %d", c.what, got, c.want)
		}
	}
	if got, _ := findOwnSkill("alice", made.ID); got.Name != "refunds" {
		t.Errorf("a refused revision changed the skill: %+v", got.SkillRecord)
	}
}
