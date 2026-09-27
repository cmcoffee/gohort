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

// A question's options can come from the place's API: the list is found in
// the usual answer shapes, read by the named field or a likely one, and a
// target that publishes through an agent has no API to ask.
func TestLiveOptionsAreReadFromTheAnswer(t *testing.T) {
	parse := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		body, key string
		want      []string
	}{
		{`[{"id":3,"name":"News"},{"id":4,"name":"Guides"}]`, "", []string{"News", "Guides"}},
		{`[{"id":3,"name":"News","slug":"news"}]`, "slug", []string{"news"}},
		{`{"data":[{"title":"Spaces"},{"title":"Spaces"},{"title":"Wiki"}]}`, "", []string{"Spaces", "Wiki"}},
		{`{"total":2,"channels":["general","random"]}`, "", []string{"general", "random"}},
		{`[{"id":7,"title":{"rendered":"Release notes"}}]`, "", []string{"Release notes"}},
		{`{"ok":true}`, "", nil},
	}
	for _, c := range cases {
		got := extractOptions(parse(c.body), c.key)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s (%q): got %q, want %q", c.body, c.key, got, c.want)
		}
	}

	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Wiki", Uses: "agent", Agent: "a1", Instructions: "Add a page.",
		Fields: []TargetField{{Label: "Space", OptionsFrom: "/spaces name"}}})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "w1"
	app.targetsDB("alice").Set(targetTable, tgt.ID, tgt)
	if f := tgt.Fields[0].field(); f.Type != "select" || f.OptionsFrom != "/spaces name" {
		t.Errorf("a live question is a pick-one that names where its list comes from: %+v", f)
	}
	d := &targetsDest{app: app}
	if _, err := d.FieldOptions(context.Background(), "alice", "target:w1", "space"); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Errorf("an agent target has no API to fetch from: %v", err)
	}
	if _, err := d.FieldOptions(context.Background(), "alice", "target:w1", "nope"); err == nil {
		t.Error("an unknown question must be refused")
	}
}

// A publish surface with no targets links to where they are made: the
// Extensions section, by the slug its rail answers to.
func TestTheSetupLinkLandsOnTheTargetsSection(t *testing.T) {
	if got := docs.PublishTargetsSetupURL(); got != "/extensions#publishing-targets" {
		t.Errorf("setup link: got %q", got)
	}
}

// The Publisher publishes to a target through its tools: it sees the target's
// questions, passes answers, files the record under the target's own kind (the
// one the quick Update path reads), and an update reuses the answers given
// before.
func TestThePublisherPublishesToATargetWithItsAnswers(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Team blog", Credential: "blogapi", Instructions: "Create a draft post.",
		Fields: []TargetField{{Label: "Category", Options: "News, Guides", Required: "yes"}}})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "tb"
	app.targetsDB("carol").Set(targetTable, tgt.ID, tgt)
	docs.RegisterPublishDestination(&targetsDest{app: app})
	var gotInstr string
	docs.RegisterCredentialPublisher(func(_ context.Context, _, _, instr string) (string, string, error) {
		gotInstr = instr
		return "Posted.", "https://blog.example/p/1", nil
	})

	var records []docs.PublishRecord
	open := func() (Document, bool) {
		return Document{Doc: docs.PublishDoc{Title: "Launch", Markdown: "# Launch"}, Records: records,
			Save: func(r docs.PublishRecord) error { records = docs.UpsertPublishRecord(records, r); return nil }}, true
	}
	tools := map[string]AgentToolDef{}
	for _, td := range BuildPublishTools(context.Background(), "carol", open) {
		tools[td.Tool.Name] = td
	}
	ctx := context.Background()
	list, err := tools["list_publish_targets"].Handler(ctx, map[string]any{"destination": "target:"})
	if err != nil || !strings.Contains(list, "asks category (Category): one of News | Guides, required") {
		t.Errorf("the target's questions are listed under it: %v\n%s", err, list)
	}
	if _, err := tools["publish_document"].Handler(ctx, map[string]any{"destination": "target:", "target": "tb"}); err == nil || !strings.Contains(err.Error(), "Category") {
		t.Errorf("a required question left unanswered comes back for the Publisher to ask: %v", err)
	}
	if _, err := tools["publish_document"].Handler(ctx, map[string]any{"destination": "target:", "target": "tb",
		"answers": map[string]any{"category": "News"}}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Kind != "target:tb" || records[0].Answers["category"] != "News" || records[0].URL != "https://blog.example/p/1" {
		t.Fatalf("the record is filed under the target's own kind with its answers: %+v", records)
	}
	if _, err := tools["publish_document"].Handler(ctx, map[string]any{"destination": "target:tb", "update_existing": true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotInstr, "Category: News") || !strings.Contains(gotInstr, "https://blog.example/p/1") {
		t.Errorf("an update reuses the answers and points at the page it made:\n%s", gotInstr)
	}
}
