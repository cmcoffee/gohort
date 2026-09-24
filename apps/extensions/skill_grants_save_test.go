package extensions

// The skill editor is a form plus three pickers against one record, and each
// posts back what it loaded when the row opened. The sequence that lost work:
// open the row, flip a chip, then Save the form. The form's stale copy of the
// grant went back over the chip.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestSavingTheSkillFormKeepsAChipFlippedSinceItOpened(t *testing.T) {
	app, req := authedExtensions(t)
	made, err := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "pdf", Instructions: "extract", AllowedTools: []string{"old_tool"}})
	if err != nil {
		t.Fatal(err)
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

	// The row opens: the form loads its view.
	w := call("GET", "/api/skills?id="+made.ID+"&view=form", "")
	var form map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &form); err != nil {
		t.Fatalf("form load: %d %s", w.Code, w.Body.String())
	}
	for _, k := range []string{"allowed_tools", "attached_collections", "allowed_users"} {
		if _, has := form[k]; has {
			t.Errorf("the form's load carries %s, so Save would write it back stale", k)
		}
	}

	// A chip flips: the picker sends its own field.
	if w := call("PATCH", "/api/skills?id="+made.ID, `{"allowed_tools":["new_tool"]}`); w.Code/100 != 2 {
		t.Fatalf("picker: %d %s", w.Code, w.Body.String())
	}

	// Save posts back the whole record the form loaded, text edited.
	form["instructions"] = "summarize"
	body, _ := json.Marshal(form)
	if w := call("POST", "/api/skills?id="+made.ID, string(body)); w.Code/100 != 2 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}

	for _, s := range LoadSkills(AuthDB(), "alice") {
		if s.ID != made.ID {
			continue
		}
		if strings.Join(s.AllowedTools, ",") != "new_tool" {
			t.Errorf("Save reverted the chip: %v", s.AllowedTools)
		}
		if s.Instructions != "summarize" {
			t.Errorf("the form's edit should land: %q", s.Instructions)
		}
		return
	}
	t.Fatal("skill gone")
}

func TestASkillPickerChangesOnlyItsOwnField(t *testing.T) {
	app, req := authedExtensions(t)
	made, _ := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "pdf", Instructions: "extract",
		AllowedTools: []string{"t1"}, AllowedUsers: []string{"bo"}})
	patch := func(body string) int {
		r := req("PATCH", "/api/skills?id="+made.ID)
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleUserSkills(w, r)
		return w.Code
	}
	if c := patch(`{"attached_collections":["c1"]}`); c/100 != 2 {
		t.Fatalf("patch: %d", c)
	}
	s := LoadSkills(AuthDB(), "alice")[0]
	if strings.Join(s.AttachedCollections, ",") != "c1" || strings.Join(s.AllowedTools, ",") != "t1" ||
		strings.Join(s.AllowedUsers, ",") != "bo" || s.Instructions != "extract" {
		t.Errorf("one picker moved another's field: %+v", s)
	}
	if c := patch(`{"allowed_users":[]}`); c/100 != 2 {
		t.Fatalf("clear: %d", c)
	}
	if s := LoadSkills(AuthDB(), "alice")[0]; len(s.AllowedUsers) != 0 {
		t.Errorf("an empty list has to clear the share: %v", s.AllowedUsers)
	}
	if c := patch(`{"name":"renamed"}`); c != http.StatusBadRequest {
		t.Errorf("a PATCH with no grant in it should be refused: %d", c)
	}
}
