package admin

// The admin skill editor is an auto-saving form plus two pickers. Every save
// used to rebuild the skill from the body alone, and none of the three carries
// the share list or the bundled tools, so any edit here dropped both; and the
// form's stale copy of a list went back over a chip flipped since the row
// opened.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAdminSkillEditsKeepWhatTheyDoNotCarry(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return db }
	RootDB = db
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	db.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	token := AuthCreateSession(db, "root")

	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerSkillsRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	made, err := SaveSkill(db, "root", SkillRecord{Name: "pdf", Description: "d", Instructions: "extract",
		AllowedTools: []string{"t1"}, AllowedUsers: []string{"bo"}, Playbook: []PlaybookRule{{Fact: "f", How: "h", Then: "x", Else: "y"}}})
	if err != nil {
		t.Fatal(err)
	}

	w := call("GET", "/api/skills/"+made.ID+"?view=form", "")
	var form map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &form); err != nil {
		t.Fatalf("form load: %d %s", w.Code, w.Body.String())
	}
	if _, has := form["allowed_tools"]; has {
		t.Error("the form's load carries allowed_tools, so each save writes it back stale")
	}

	if w := call("PATCH", "/api/skills?id="+made.ID, `{"allowed_tools":["t2"]}`); w.Code/100 != 2 {
		t.Fatalf("picker: %d %s", w.Code, w.Body.String())
	}
	form["instructions"] = "summarize"
	body, _ := json.Marshal(form)
	if w := call("POST", "/api/skills", string(body)); w.Code/100 != 2 {
		t.Fatalf("form save: %d %s", w.Code, w.Body.String())
	}

	s := LoadSkills(db, "root")[0]
	if strings.Join(s.AllowedTools, ",") != "t2" {
		t.Errorf("the form reverted the chip: %v", s.AllowedTools)
	}
	if strings.Join(s.AllowedUsers, ",") != "bo" {
		t.Errorf("an edit dropped the share list: %v", s.AllowedUsers)
	}
	if s.Instructions != "summarize" || len(s.Playbook) != 1 {
		t.Errorf("the form's edit should land and the playbook stay: %q %d", s.Instructions, len(s.Playbook))
	}

	// Emptying the playbook box still clears the rules.
	form["playbook_text"] = ""
	body, _ = json.Marshal(form)
	if w := call("POST", "/api/skills", string(body)); w.Code/100 != 2 {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if s := LoadSkills(db, "root")[0]; len(s.Playbook) != 0 {
		t.Errorf("an empty playbook box has to clear the rules: %d left", len(s.Playbook))
	}
}
