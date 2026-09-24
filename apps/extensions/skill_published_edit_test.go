package extensions

// A skill its author published leaves their pool for the deployment's, and the
// author's Skills table lists it with Edit and Disable on the row. Those looked
// only in the author's own pool, so every one of them answered 404.

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

func TestAnAuthorCanEditAndMuteTheirPublishedSkill(t *testing.T) {
	app, req := authedExtensions(t)
	made, err := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "pdf", Instructions: "extract"})
	if err != nil {
		t.Fatal(err)
	}
	// Published the way it really is: a request, then an admin's approval.
	if err := promotion.CreatePromotionRequest(AuthDB(), "alice", SkillPromotionKind, "pdf", ""); err != nil {
		t.Fatal(err)
	}
	if err := promotion.Approve(AuthDB(), promotion.RequestKey(SkillPromotionKind, "alice", "pdf"), "admin"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := req(method, path)
		if body != "" {
			r.Body = io.NopCloser(strings.NewReader(body))
		}
		w := httptest.NewRecorder()
		app.handleUserSkills(w, r)
		return w
	}

	// Edit opens.
	w := call("GET", "/api/skills?id="+made.ID+"&view=form", "")
	if w.Code != 200 {
		t.Fatalf("the Edit form cannot load a published skill: %d %s", w.Code, w.Body.String())
	}
	var form map[string]any
	json.Unmarshal(w.Body.Bytes(), &form)

	// Edit saves, into the deployment's copy.
	form["instructions"] = "summarize"
	body, _ := json.Marshal(form)
	if w := call("POST", "/api/skills?id="+made.ID, string(body)); w.Code/100 != 2 {
		t.Fatalf("the Edit form cannot save a published skill: %d %s", w.Code, w.Body.String())
	}
	if got := DeploymentSkills(AuthDB()); len(got) != 1 || got[0].Instructions != "summarize" {
		t.Fatalf("the edit did not reach the deployment's copy: %+v", got)
	}
	if got := LoadSkills(AuthDB(), "alice"); len(got) != 0 {
		t.Errorf("the edit made a private copy: %+v", got)
	}

	// A chip picker saves too.
	if w := call("PATCH", "/api/skills?id="+made.ID, `{"allowed_tools":["t1"]}`); w.Code/100 != 2 {
		t.Fatalf("a picker cannot save a published skill: %d %s", w.Code, w.Body.String())
	}

	// Disable mutes it for everybody, and the row stays in the author's list so
	// Enable has somewhere to be.
	if w := call("POST", "/api/skills?action=disable&id="+made.ID, ""); w.Code/100 != 2 {
		t.Fatalf("Disable on a published skill: %d %s", w.Code, w.Body.String())
	}
	if got := DeploymentSkills(AuthDB()); len(got) != 0 {
		t.Errorf("a muted published skill still activates: %+v", got)
	}
	var rows []map[string]any
	json.Unmarshal(call("GET", "/api/skills", "").Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["disabled"] != true || rows[0]["published"] != true {
		t.Fatalf("the muted published skill left its author's list: %+v", rows)
	}
	if w := call("POST", "/api/skills?action=enable&id="+made.ID, ""); w.Code/100 != 2 {
		t.Fatalf("Enable on a published skill: %d %s", w.Code, w.Body.String())
	}
	if got := DeploymentSkills(AuthDB()); len(got) != 1 || got[0].Instructions != "summarize" ||
		strings.Join(got[0].AllowedTools, ",") != "t1" {
		t.Errorf("the published skill did not come back as edited: %+v", got)
	}
}
