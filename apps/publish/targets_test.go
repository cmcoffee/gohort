package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A target publishes through its credential with its instruction, title,
// answers and the document; a required question left empty stops it, and an
// earlier address rides along as "update that one".
func TestATargetPublishesThroughItsIntegration(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Team blog", Credential: "blogapi", Instructions: "Create a draft post.",
		Fields: []TargetField{{Label: "Category", Options: "News, Guides", Required: "yes"}}})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "t1"
	app.targetsDB("alice").Set(targetTable, tgt.ID, tgt)

	var gotCred, gotInstr string
	docs.RegisterCredentialPublisher(func(_ context.Context, user, cred, instr string) (string, string, error) {
		gotCred, gotInstr = cred, instr
		return "Draft post created.", "https://blog.example/p/9", nil
	})
	d := &targetsDest{app: app}
	req := docs.PublishRequest{Target: "t1", Title: "Launch notes", Doc: docs.PublishDoc{Markdown: "# Launch\n\nBody."}}
	if _, err := d.Publish(context.Background(), "alice", req); err == nil || !strings.Contains(err.Error(), "Category") {
		t.Errorf("the required question must be answered first: %v", err)
	}
	req.Answers = map[string]string{"category": "News"}
	req.ExternalID = "https://blog.example/p/8"
	res, err := d.Publish(context.Background(), "alice", req)
	if err != nil {
		t.Fatal(err)
	}
	if gotCred != "blogapi" || res.URL != "https://blog.example/p/9" || res.ExternalID != res.URL {
		t.Errorf("published through the credential, address kept for the next update: %q %+v", gotCred, res)
	}
	for _, want := range []string{"Create a draft post.", "Title: Launch notes", "Category: News", "published here before, at https://blog.example/p/8", "# Launch"} {
		if !strings.Contains(gotInstr, want) {
			t.Errorf("the pass should be handed %q:\n%s", want, gotInstr)
		}
	}
	specs := d.TargetSpecs(context.Background(), "alice")
	if len(specs) != 1 || specs[0].Kind != "target:t1" || specs[0].Fields[0].Type != "select" || !specs[0].Fields[0].Required {
		t.Errorf("a question with options is a required pick-one: %+v", specs)
	}
}

// The Extensions section's API: create, list, edit (who may use it is kept
// from the pills, not the form), and delete. A target missing what it needs
// is refused with what is missing.
func TestTheTargetControls(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	token := AuthCreateSession(adb, "alice")
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		app.handleTargets(w, r)
		return w
	}
	if w := do("POST", "/api/targets", map[string]any{"label": "Blog", "uses": "api", "instructions": "Post it."}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "API integration") {
		t.Errorf("a target with no integration is refused naming it: %d %s", w.Code, w.Body)
	}
	w := do("POST", "/api/targets", map[string]any{"label": "Blog", "uses": "api", "credential": "blogapi", "instructions": "Post it."})
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var made struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &made)
	tg, _ := app.loadTarget("alice", made.ID)
	tg.Agents = []string{"agent-1"}
	app.targetsDB("alice").Set(targetTable, tg.ID, tg)
	if w := do("POST", "/api/targets", map[string]any{"id": made.ID, "label": "Team blog", "uses": "api", "credential": "blogapi", "instructions": "Post it as a draft."}); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if got, _ := app.loadTarget("alice", made.ID); got.Label != "Team blog" || len(got.Agents) != 1 {
		t.Errorf("an edit changes the form's fields and keeps who may use it: %+v", got)
	}
	var rows []map[string]any
	json.Unmarshal(do("GET", "/api/targets", nil).Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["uses"] != "API: blogapi" {
		t.Errorf("list: %+v", rows)
	}
	if w := do("DELETE", "/api/targets/"+made.ID, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if len(app.listTargets("alice")) != 0 {
		t.Error("the target should be gone")
	}
}
