package extensions

// One name, one skill, per person. A second skill under a name the author
// already has, in their pool or published, is a name that picks neither.

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

func TestTheSkillsFormRefusesANameYouHave(t *testing.T) {
	app, req := authedExtensions(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := req(method, path)
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleUserSkills(w, r)
		return w
	}
	if w := call("POST", "/api/skills", `{"name":"Triage","description":"d","instructions":"x"}`); w.Code/100 != 2 {
		t.Fatalf("first create: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/skills", `{"name":"triage","description":"d","instructions":"y"}`); w.Code != 409 {
		t.Errorf("a second create under a name the user has: %d %s", w.Code, w.Body.String())
	}
	if got := LoadSkills(AuthDB(), "alice"); len(got) != 1 {
		t.Fatalf("want one skill, have %+v", got)
	}

	// Published names count: the name still picks that skill.
	if err := promotion.CreatePromotionRequest(AuthDB(), "alice", SkillPromotionKind, "Triage", ""); err != nil {
		t.Fatal(err)
	}
	if err := promotion.Approve(AuthDB(), promotion.RequestKey(SkillPromotionKind, "alice", "Triage"), "admin"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if w := call("POST", "/api/skills", `{"name":"Triage","description":"d","instructions":"z"}`); w.Code != 409 {
		t.Errorf("a create under a name the user published: %d %s", w.Code, w.Body.String())
	}

	// A rename onto a taken name is a create by another route; an edit that
	// keeps its own name is not refused.
	other, _ := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "Ledger", Description: "d", Instructions: "x"})
	if w := call("POST", "/api/skills?id="+other.ID, `{"name":"Triage","description":"d","instructions":"x"}`); w.Code != 409 {
		t.Errorf("a rename onto a taken name: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/skills?id="+other.ID, `{"name":"Ledger","description":"d2","instructions":"x"}`); w.Code/100 != 2 {
		t.Errorf("an edit keeping its name was refused: %d %s", w.Code, w.Body.String())
	}
}
