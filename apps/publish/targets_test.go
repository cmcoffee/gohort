package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	var gotCred, gotInstr, gotBody string
	docs.RegisterCredentialPublisher(func(ctx context.Context, user, cred, instr string) (string, string, error) {
		gotCred, gotInstr = cred, instr
		if b := docs.PublishBodyFrom(ctx); b != nil {
			gotBody = b.Markdown()
		}
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
	for _, want := range []string{"Create a draft post.", "Title: Launch notes", "Category: News", "ALREADY published here, at https://blog.example/p/8", "Never create a new one", docs.BodyMarkdownPlaceholder} {
		if !strings.Contains(gotInstr, want) {
			t.Errorf("the pass should be handed %q:\n%s", want, gotInstr)
		}
	}
	// The document rides by reference, not in the instruction.
	if strings.Contains(gotInstr, "\n---\n") || gotBody != "# Launch\n\nBody." {
		t.Errorf("the document went in the instruction, or not on the context: body %q\n%s", gotBody, gotInstr)
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
		mux := http.NewServeMux()
		app.SetWebMux(mux, "")
		app.Routes()
		mux.ServeHTTP(w, r)
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

// The Publisher sees each of the person's targets by name with its own
// destination id, ahead of everything else, and the destinations it cannot
// use last, said as such. As one "Your publishing targets" entry beside the
// deployment's unconfigured Confluence, "publish to my Confluence target"
// went to Confluence and came back as not configured.
func TestThePublisherSeesTargetsByNameBeforeWhatItCannotUse(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Team Confluence", Desc: "Atlassian, through MCP", Credential: docs.MCPIntegrationPrefix + "atlassian", Instructions: "Create a page."})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "tc"
	app.targetsDB("dana").Set(targetTable, tgt.ID, tgt)
	docs.RegisterPublishDestination(&targetsDest{app: app})
	docs.RegisterPublishDestination(&confluenceDest{app: app}) // no credential: not available
	open := func() (Document, bool) {
		return Document{Doc: docs.PublishDoc{Title: "Runbook", Markdown: "# Runbook"}}, true
	}
	tools := map[string]AgentToolDef{}
	for _, td := range BuildPublishTools(context.Background(), "dana", open) {
		tools[td.Tool.Name] = td
	}
	out, err := tools["list_publish_destinations"].Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	mine := strings.Index(out, "Team Confluence (destination id: target:tc), available")
	off := strings.Index(out, "Not available, do not publish to these:")
	builtin := strings.Index(out, "Confluence (destination id: confluence):")
	if mine < 0 || off < 0 || builtin < 0 || !(mine < off && off < builtin) {
		t.Fatalf("the target must come first by name, the unconfigured Confluence last as unusable:\n%s", out)
	}
	if strings.Contains(out, "Your publishing targets (destination id: target:)") {
		t.Fatalf("the targets are still one anonymous entry:\n%s", out)
	}
}

// A destination that cannot be used sends the person to a target of their
// own, in Extensions, never to Admin > Publishing: that page is an admin's,
// and the person reading the reason usually is not one.
func TestUnavailableDestinationsPointAtYourOwnTargets(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	for name, d := range map[string]docs.PublishDestination{"confluence": &confluenceDest{app: app}, "webhook": &webhookDest{app: app}} {
		ok, why := d.Available("erin")
		if ok || !strings.Contains(why, "Extensions, Publishing targets") || strings.Contains(why, "Admin") {
			t.Errorf("%s: available=%v why=%q", name, ok, why)
		}
	}
}

// publish_document's result carries what the publish did, step by step, so
// its card still shows the steps once the call ends, and a failure says how
// far it got. Anyone already watching the publish's steps still gets them.
func TestAPublishCallKeepsItsSteps(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Team wiki", Credential: "wikiapi", Instructions: "Create a page."})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "tw"
	app.targetsDB("erin").Set(targetTable, tgt.ID, tgt)
	docs.RegisterPublishDestination(&targetsDest{app: app})
	fail := false
	docs.RegisterCredentialPublisher(func(ctx context.Context, _, _, _ string) (string, string, error) {
		docs.PublishStep(ctx, "Looked up the space")
		if fail {
			docs.PublishStep(ctx, "  failed: 403 forbidden")
			return "", "", fmt.Errorf("the wiki refused the page")
		}
		docs.PublishStep(ctx, "Created the page")
		return "Page created.", "https://wiki.example/p/7", nil
	})
	open := func() (Document, bool) {
		return Document{Doc: docs.PublishDoc{Title: "Runbook", Markdown: "# Runbook"}}, true
	}
	var publishTool AgentToolDef
	for _, td := range BuildPublishTools(context.Background(), "erin", open) {
		if td.Tool.Name == "publish_document" {
			publishTool = td
		}
	}
	var watched []string
	ctx := docs.WithPublishSteps(context.Background(), func(s string) { watched = append(watched, s) })

	out, err := publishTool.Handler(ctx, map[string]any{"destination": "target:tw"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Steps:\n- Looked up the space\n- Created the page") {
		t.Errorf("the result does not keep the steps:\n%s", out)
	}
	if strings.Join(watched, "|") != "Looked up the space|Created the page" {
		t.Errorf("the steps stopped reaching whoever was watching: %v", watched)
	}

	fail = true
	_, err = publishTool.Handler(context.Background(), map[string]any{"destination": "target:tw"})
	if err == nil || !strings.Contains(err.Error(), "refused the page") || !strings.Contains(err.Error(), "- Looked up the space\n-   failed: 403 forbidden") {
		t.Errorf("a failure should say how far it got: %v", err)
	}
}

// A document published before is updated in place whatever the model sends.
// Updating used to be the model's choice (update_existing), and a call that
// left it out made a second page whose record replaced the first one's, so
// the next update went to the copy. A second copy is now only new_copy=true,
// and an update that comes back without an address keeps the page it had.
func TestThePublisherUpdatesTheSamePageUnlessACopyIsAsked(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Team wiki", Credential: "wikiapi", Instructions: "Create a page."})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "tw"
	app.targetsDB("erin").Set(targetTable, tgt.ID, tgt)
	docs.RegisterPublishDestination(&targetsDest{app: app})
	var instrs []string
	reply := "https://wiki.example/p/1"
	docs.RegisterCredentialPublisher(func(_ context.Context, _, _, instr string) (string, string, error) {
		instrs = append(instrs, instr)
		return "Done.", reply, nil
	})
	var records []docs.PublishRecord
	open := func() (Document, bool) {
		return Document{Doc: docs.PublishDoc{Title: "Runbook", Markdown: "# Runbook"}, Records: records,
			Save: func(r docs.PublishRecord) error { records = docs.UpsertPublishRecord(records, r); return nil }}, true
	}
	tools := map[string]AgentToolDef{}
	for _, td := range BuildPublishTools(context.Background(), "erin", open) {
		tools[td.Tool.Name] = td
	}
	publish := func(args map[string]any) string {
		t.Helper()
		out, err := tools["publish_document"].Handler(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	publish(map[string]any{"destination": "target:tw"})
	// No flag at all: still an update of the page it made.
	reply = "" // and the integration says nothing about where it is
	if out := publish(map[string]any{"destination": "target:tw"}); !strings.HasPrefix(out, "Updated") {
		t.Errorf("a republish with no flag updates: %s", out)
	}
	if !strings.Contains(instrs[1], "https://wiki.example/p/1") {
		t.Errorf("the update points at the page it made:\n%s", instrs[1])
	}
	if len(records) != 1 || records[0].ExternalID != "https://wiki.example/p/1" || records[0].URL != "https://wiki.example/p/1" {
		t.Fatalf("an update that said nothing about where keeps the page it had: %+v", records)
	}
	publish(map[string]any{"destination": "target:tw"})
	if !strings.Contains(instrs[2], "https://wiki.example/p/1") {
		t.Errorf("so the next one updates the same page:\n%s", instrs[2])
	}

	// A separate copy is only on request, and says what happens to the first.
	reply = "https://wiki.example/p/2"
	out := publish(map[string]any{"destination": "target:tw", "new_copy": true})
	if strings.Contains(instrs[3], "https://wiki.example/p/1") || !strings.Contains(out, "separate copy") {
		t.Errorf("new_copy makes a new page and says the earlier one is left: %s\n%s", out, instrs[3])
	}
}

// An integration target keeps the page's table of contents across updates
// without the destination being named anywhere: the markup its run gave the
// first time is kept on the record and goes out again on every update, and
// the update is told so. A publish that sends the document's own list where a
// contents list exists says so instead of a bare "Published".
func TestATargetKeepsItsTableOfContentsAcrossUpdates(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Team wiki", Credential: docs.MCPIntegrationPrefix + "wiki", Instructions: "Create a page."})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "tw"
	app.targetsDB("fran").Set(targetTable, tgt.ID, tgt)
	docs.RegisterPublishDestination(&targetsDest{app: app})
	macro := "<toc-block/>"
	nameContents := true
	var instrs, sent []string
	docs.RegisterCredentialPublisher(func(ctx context.Context, _, _, instr string) (string, string, error) {
		instrs = append(instrs, instr)
		body := docs.PublishBodyFrom(ctx)
		if nameContents {
			body.SetLinks(nil, &macro)
		}
		args, _ := body.ExpandArgs(map[string]any{"body": docs.BodyMarkdownPlaceholder})
		sent = append(sent, args["body"].(string))
		return "Done.", "https://wiki.example/p/1", nil
	})
	md := "# Guide\n\n## Contents\n\n1. Setup\n2. Use\n3. Fix\n\n## 1. Setup\n\nA.\n\n## 2. Use\n\nB.\n\n## 3. Fix\n\nC."
	var records []docs.PublishRecord
	open := func() (Document, bool) {
		return Document{Doc: docs.PublishDoc{Title: "Guide", Markdown: md}, Records: records,
			Save: func(r docs.PublishRecord) error { records = docs.UpsertPublishRecord(records, r); return nil }}, true
	}
	tools := map[string]AgentToolDef{}
	for _, td := range BuildPublishTools(context.Background(), "fran", open) {
		tools[td.Tool.Name] = td
	}
	publish := func() string {
		t.Helper()
		out, err := tools["publish_document"].Handler(context.Background(), map[string]any{"destination": "target:tw"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	publish()
	if len(records) != 1 || records[0].NavContents == nil || *records[0].NavContents != macro {
		t.Fatalf("the markup the run gave is kept on the record: %+v", records)
	}
	// The update's run names nothing: the page keeps its table of contents.
	nameContents = false
	publish()
	if !strings.Contains(instrs[1], "go out as they did last time") {
		t.Errorf("the update is told its table of contents goes out as before:\n%s", instrs[1])
	}
	if !strings.Contains(sent[1], macro) || strings.Contains(sent[1], "1. Setup\n2. Use") {
		t.Errorf("the update carries the destination's table of contents, not the plain list:\n%s", sent[1])
	}
	if records[0].NavContents == nil || *records[0].NavContents != macro {
		t.Error("and it stays on the record for the next one")
	}

	// A page published before any was kept: the update is asked for it, and
	// one that still sends the plain list says so.
	records[0].NavContents, records[0].NavLinks = nil, nil
	out := publish()
	if !strings.Contains(instrs[2], "Keep the page's table of contents") {
		t.Errorf("an update with nothing kept asks for it:\n%s", instrs[2])
	}
	if !strings.Contains(out, "went out as a plain list") {
		t.Errorf("a plain list is said, not a bare Published: %s", out)
	}
}
