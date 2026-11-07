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
	"github.com/cmcoffee/gohort/core/ui"
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
		AllowedTools: []string{"t1"}, AllowedUsers: []string{"bo"}, Conditions: []SkillCase{{Kind: "if", Question: "Is it a PDF?", How: "Look at the file."}}})
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
	if s.Instructions != "summarize" || len(s.Conditions) != 1 || s.Conditions[0].Question != "Is it a PDF?" {
		t.Errorf("the form's edit should land and the conditions stay: %q %+v", s.Instructions, s.Conditions)
	}

	// An empty list clears them.
	form["conditions"] = []any{}
	body, _ = json.Marshal(form)
	if w := call("POST", "/api/skills", string(body)); w.Code/100 != 2 {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if s := LoadSkills(db, "root")[0]; len(s.Conditions) != 0 {
		t.Errorf("an empty list has to clear the conditions: %+v", s.Conditions)
	}
}

// On the skill form's conditions, the top line holds the two choices and the
// question, or the words, gets a line of its own: in the dialog a question
// beside a hidden column was squeezed to a few characters.
func TestAConditionsQuestionHasItsOwnLine(t *testing.T) {
	cols := map[string]ui.FormField{}
	for _, c := range skillConditionsField().Columns {
		cols[c.Field] = c
	}
	if !cols["question"].OwnLine || !cols["mentions"].OwnLine {
		t.Errorf("question own line %v, words own line %v", cols["question"].OwnLine, cols["mentions"].OwnLine)
	}
	if cols["kind"].OwnLine || cols["check"].OwnLine {
		t.Error("the two choices should share the top line")
	}
}
