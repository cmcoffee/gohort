package extensions

// The skill revision routes. Skills are the kind whose history is easiest to
// get wrong: they live in RootDB keyed by username rather than in a per-user
// store, so the store and the ring key both come from core rather than being
// rebuilt here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// authedExtensions returns an app whose requests authenticate as one user,
// with RootDB pinned so skillStore writes somewhere this test can read.
func authedExtensions(t *testing.T) (*Extensions, func(method, path string) *http.Request) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}

	prevAuth := AuthDB
	AuthDB = func() Database { return adb }
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { AuthDB = prevAuth; RootDB = prevRoot })

	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	token := AuthCreateSession(adb, "alice")

	app := &Extensions{}
	app.DB = root
	req := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		return r
	}
	return app, req
}

func TestSkillRevisionRoutes(t *testing.T) {
	app, req := authedExtensions(t)

	made, err := SaveSkill(AuthDB(), "alice", SkillRecord{Name: "pdf", Instructions: "extract text first"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := made
	edited.Instructions = "summarize first"
	if _, err := SaveSkillAs(AuthDB(), "alice", edited, "edited instructions"); err != nil {
		t.Fatalf("edit: %v", err)
	}

	w := httptest.NewRecorder()
	app.handleUserSkillOne(w, req("GET", "/api/skills/"+made.ID+"/revisions"))
	if w.Code != 200 {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Empty   string `json:"empty"`
		Entries []struct {
			Title   string `json:"title"`
			Detail  string `json:"detail"`
			Actions []struct {
				URL  string `json:"url"`
				Kind string `json:"kind"`
			} `json:"actions"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}
	if len(got.Entries) != 1 || got.Entries[0].Detail != "edited instructions" {
		t.Fatalf("entries = %+v", got.Entries)
	}
	if !strings.Contains(got.Empty, "skill") {
		t.Errorf("the empty state must name the thing: %q", got.Empty)
	}

	w = httptest.NewRecorder()
	app.handleUserSkillOne(w, req("GET", "/api/skills/"+made.ID+"/revisions/preview?rev=1"))
	if w.Code != 200 {
		t.Fatalf("preview status %d: %s", w.Code, w.Body.String())
	}
	var view struct{ Title, Text string }
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(view.Text, "extract text first") {
		t.Errorf("the kept instructions must be readable: %q", view.Text)
	}

	w = httptest.NewRecorder()
	app.handleUserSkillOne(w, req("POST", "/api/skills/"+made.ID+"/revisions/restore?rev=1"))
	if w.Code != 200 {
		t.Fatalf("restore status %d: %s", w.Code, w.Body.String())
	}
	for _, s := range LoadSkills(AuthDB(), "alice") {
		if s.ID == made.ID && s.Instructions != "extract text first" {
			t.Errorf("restored instructions = %q", s.Instructions)
		}
	}
}

// A skill id that is not in the caller's own pool is not theirs to read.
func TestSkillRevisionsRefuseAnotherUsersSkill(t *testing.T) {
	app, req := authedExtensions(t)

	made, err := SaveSkill(AuthDB(), "bob", SkillRecord{Name: "bob's", Instructions: "v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := made
	edited.Instructions = "v2"
	if _, err := SaveSkill(AuthDB(), "bob", edited); err != nil {
		t.Fatalf("edit: %v", err)
	}

	for _, path := range []string{
		"/api/skills/" + made.ID + "/revisions",
		"/api/skills/" + made.ID + "/revisions/preview?rev=1",
	} {
		w := httptest.NewRecorder()
		app.handleUserSkillOne(w, req("GET", path))
		if w.Code == 200 {
			t.Errorf("%s served another user's history", path)
		}
	}
	w := httptest.NewRecorder()
	app.handleUserSkillOne(w, req("POST", "/api/skills/"+made.ID+"/revisions/restore?rev=1"))
	if w.Code == 200 {
		t.Error("another user's skill was restored")
	}
	for _, s := range LoadSkills(AuthDB(), "bob") {
		if s.ID == made.ID && s.Instructions != "v2" {
			t.Errorf("another user's skill changed: %q", s.Instructions)
		}
	}
}
