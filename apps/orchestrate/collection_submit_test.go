package orchestrate

// Submitters and versions (collection_submit.go): the owner's agents that may
// add to a collection, checked or always allowed, and documents kept as older
// versions of the ones that replaced them.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// The page lists and saves the submitters: an agent stored as curator by an
// earlier version shows as checked and moves into the list on the first save;
// only the owner's own agents may be named.
func TestSubmittersAreTheOwnersAgentsWithTheirTrust(t *testing.T) {
	df := curateFixture(t)
	saveCollection(df.udb, Collection{ID: "col-s", Name: "S", Owner: "u", CuratorAgent: df.agent.ID})
	got, err := df.app.collectionSubmitters(curateCall(df, http.MethodGet, "u", "col-s"))
	if err != nil {
		t.Fatal(err)
	}
	rows := got.(map[string]any)["submitters"].([]submitterRow)
	if len(rows) != 1 || rows[0].Agent != df.agent.ID || rows[0].Trust != trustChecked {
		t.Fatalf("an earlier curator should read as a checked submitter: %+v", rows)
	}
	save := func(rows string) error {
		_, err := df.app.collectionSubmittersSave(curateCall(df, http.MethodPost, "u", "col-s"), struct {
			Submitters json.RawMessage `json:"submitters"`
		}{json.RawMessage(rows)})
		return err
	}
	if err := save(`[{"agent":"` + df.agent.ID + `","trust":"always"}]`); err != nil {
		t.Fatal(err)
	}
	c, _ := loadCollection(df.udb, "u", "col-s")
	if c.Submitters[df.agent.ID] != trustAlways || c.CuratorAgent != "" {
		t.Errorf("saved: %+v %q", c.Submitters, c.CuratorAgent)
	}
	if got := submittableCollectionsFor(df.udb, "u", df.agent.ID); len(got) != 1 || submitterTrust(got[0], df.agent.ID) != trustAlways {
		t.Errorf("the agent should submit to it, always allowed: %+v", got)
	}
	if save(`[{"agent":"nobody-of-yours","trust":"checked"}]`) == nil {
		t.Error("an agent that is not the owner's was named")
	}
	const hidden = "app-test-hidden-submitter"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{ID: hidden, OwningApp: "Test", Name: "Hidden", Prompt: "p", Hidden: true})
	if save(`[{"agent":"`+hidden+`","trust":"always"}]`) == nil {
		t.Error("an app's hidden agent was named a submitter")
	}
	if err := save(`[]`); err != nil {
		t.Fatal(err)
	}
	if got := submittableCollectionsFor(df.udb, "u", df.agent.ID); len(got) != 0 {
		t.Errorf("an agent taken off still submits: %+v", got)
	}
}

// An always-allowed submitter's page goes straight in, no check asked, and
// can name the document it replaces: that one is kept and marked as the
// older version, ranked below, and shown as such on the documents list.
func TestAnAlwaysAllowedSubmissionGoesInAndMarksWhatItReplaces(t *testing.T) {
	const page = "https://example.com/guide-v4"
	f := vetFixture(t, map[string]string{page: "Admin guide 4: the audit log is on by default."})
	f.c.Submitters = map[string]string{"sub": trustAlways}
	add, _ := f.T.submitTool([]Collection{f.c}, "sub").(*GroupedTool).Action("add_url")
	out, err := add.Handler(map[string]any{"url": page, "supersedes": "Configuration reference"}, &ToolSession{DB: f.store("alice"), Username: "alice"})
	if err != nil || !strings.Contains(out, "Ingested") || !strings.Contains(out, "marked as the older version") {
		t.Fatalf("%q %v", out, err)
	}
	if n := f.llm.Calls(); n != 0 {
		t.Errorf("an always-allowed page was checked: %d model calls", n)
	}
	var newDoc string
	for doc := range f.docs() {
		if doc != "doc-old" {
			newDoc = doc
		}
	}
	for _, ch := range f.T.docChunks(f.c, "doc-old") {
		if ch.Reason != RetireSuperseded || ch.Successor != newDoc {
			t.Fatalf("the old guide should be marked as replaced by the new one: %+v", ch.MemoryProvenance)
		}
	}
	// The documents list's rows are a type local to its handler: read the
	// JSON a page gets.
	list, _ := f.T.collectionSources(f.call(http.MethodGet, ""))
	raw, _ := json.Marshal(list)
	if !strings.Contains(string(raw), `"superseded_by":"`+newDoc+`"`) {
		t.Errorf("the documents list should say the old guide was replaced: %s", raw)
	}
}

// A checked submitter's file goes through the check before anything is
// stored: one that contradicts the collection is turned away.
func TestACheckedSubmittersFileIsCheckedFirst(t *testing.T) {
	f := vetFixture(t, nil, FakeTurn{Content: vetReply("conflicts", 1, "on vs off")})
	ws := t.TempDir()
	body := strings.Repeat("The audit log is on by default in every release. ", 8)
	if err := os.WriteFile(filepath.Join(ws, "notes.md"), []byte("# Notes\n\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	add, _ := f.T.submitTool([]Collection{f.c}, "sub").(*GroupedTool).Action("add_file")
	out, err := add.Handler(map[string]any{"path": "notes.md"}, &ToolSession{DB: f.store("alice"), Username: "alice", WorkspaceDir: ws})
	if err != nil || !strings.Contains(out, "Not added") {
		t.Fatalf("%q %v", out, err)
	}
	if len(f.docs()) != 1 {
		t.Errorf("a file that did not pass went in: %v", f.docs())
	}
}

// The owner or a maintainer sets and clears which document replaced one; a
// document whose replacement is removed is the current version again.
func TestAVersionIsSetClearedAndForgotten(t *testing.T) {
	f := vetFixture(t, nil)
	IngestDocument(context.Background(), VectorDB, collectionSource(f.c.ID), "doc-new", "Configuration reference v4", "## Configuration reference v4\n\n"+strings.Repeat("The audit log is on by default. ", 20))
	set := func(doc, by string) error {
		r := httptest.NewRequest(http.MethodPost, "/api/collections/col-v/sources/"+doc+"/superseded", nil)
		r.SetPathValue("id", "col-v")
		r.SetPathValue("doc", doc)
		_, err := f.T.collectionSuperseded(&jsonapi.Call{W: httptest.NewRecorder(), R: r, User: "alice", UDB: f.store("alice")}, struct {
			By string `json:"by"`
		}{by})
		return err
	}
	marked := func() string {
		for _, ch := range f.T.docChunks(f.c, "doc-old") {
			if ch.Reason == RetireSuperseded {
				return ch.Successor
			}
		}
		return ""
	}
	if err := set("doc-old", "doc-new"); err != nil || marked() != "doc-new" {
		t.Fatalf("set: %v %q", err, marked())
	}
	if set("doc-old", "doc-old") == nil || set("doc-old", "no-such-doc") == nil {
		t.Error("a document was marked as replaced by itself, or by nothing there")
	}
	if err := set("doc-old", ""); err != nil || marked() != "" {
		t.Fatalf("clear: %v %q", err, marked())
	}
	set("doc-old", "doc-new")
	r := httptest.NewRequest(http.MethodDelete, "/api/collections/col-v/sources/doc-new", nil)
	r.SetPathValue("id", "col-v")
	r.SetPathValue("doc", "doc-new")
	if _, err := f.T.collectionSourceDelete(&jsonapi.Call{W: httptest.NewRecorder(), R: r, User: "alice", UDB: f.store("alice")}, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if marked() != "" {
		t.Error("with its replacement removed, the old document should be current again")
	}
}

// A plan run that finds a newer version of a document keeps the old one and
// marks it, rather than proposing its removal.
func TestARunMarksWhatANewerVersionReplaces(t *testing.T) {
	const newer = "https://docs.vendor.example/v4"
	f := vetFixture(t, map[string]string{newer: "As of version 4 the audit log is on by default."},
		FakeTurn{Content: vetReply("supersedes", 1, "v4 on, v3 off")})
	prev := collectionWebSearch
	collectionWebSearch = func(string) string { return "1. Newer\n   " + newer + "\n   x\n" }
	t.Cleanup(func() { collectionWebSearch = prev })
	f.c.SearchTerms = []string{"audit log default"}
	out, err := f.T.runPlan(context.Background(), f.store("alice"), f.c, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kept, marked as the older version") {
		t.Errorf("outcome %q", out)
	}
	chunks := f.T.docChunks(f.c, "doc-old")
	if len(chunks) == 0 || chunks[0].Reason != RetireSuperseded {
		t.Errorf("the older document should be kept and marked: %+v", chunks)
	}
}

// A checked submission the check finds to be a newer version of a document
// goes in, and the older document is kept and marked.
func TestACheckedSubmissionMarksWhatTheCheckFoundItReplaces(t *testing.T) {
	const page = "https://docs.vendor.example/v4"
	f := vetFixture(t, map[string]string{page: "As of version 4 the audit log is on by default."},
		FakeTurn{Content: vetReply("supersedes", 1, "v4 on, v3 off")})
	add, _ := f.T.submitTool([]Collection{f.c}, "sub").(*GroupedTool).Action("add_url")
	out, err := add.Handler(map[string]any{"url": page}, &ToolSession{DB: f.store("alice"), Username: "alice"})
	if err != nil || !strings.Contains(out, "marked as the older version") {
		t.Fatalf("%q %v", out, err)
	}
	if chunks := f.T.docChunks(f.c, "doc-old"); len(chunks) == 0 || chunks[0].Reason != RetireSuperseded {
		t.Errorf("the older document should be marked: %+v", chunks)
	}
}
