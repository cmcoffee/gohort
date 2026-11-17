package anvil

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// knowledgeWorld is alice's collection, bob's private one, and one everyone
// has.
func knowledgeWorld(t *testing.T) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	SaveCollection(UserDB(CollectionsDB(), "alice"), Collection{ID: "api-docs", Owner: "alice", Name: "API docs"})
	SaveCollection(UserDB(CollectionsDB(), "bob"), Collection{ID: "bobs", Owner: "bob", Name: "Bob's notes"})
	RootDB.Set(GlobalCollectionsTable, "standards", Collection{ID: "standards", Name: "Standards", Scope: CollectionScopeDeployment})
}

// A project searches only knowledge the person asking may read: their own,
// and everyone's; never somebody else's private collection, which attaching
// it by id would otherwise hand over.
func TestAProjectSearchesOnlyKnowledgeThePersonMayRead(t *testing.T) {
	knowledgeWorld(t)
	got := readableCollections("alice", []string{"api-docs", "bobs", " ", "standards", "api-docs", "gone"})
	if strings.Join(got, ",") != "api-docs,standards" {
		t.Errorf("alice reads %q", got)
	}
	if got := readableCollections("bob", []string{"api-docs", "bobs"}); strings.Join(got, ",") != "bobs" {
		t.Errorf("bob reads %q", got)
	}
	var labels []string
	for _, o := range collectionOptions("alice") {
		labels = append(labels, o.Value+"="+o.Label)
	}
	all := strings.Join(labels, "|")
	if !strings.Contains(all, "api-docs=API docs") || !strings.Contains(all, "standards=Standards (everyone's)") || strings.Contains(all, "bobs") {
		t.Errorf("alice is offered %q", all)
	}
}

// A save keeps only what the person saving may read.
func TestASaveKeepsOnlyReadableKnowledge(t *testing.T) {
	knowledgeWorld(t)
	T, raw := bundleRoutes(t)
	udb := UserDB(T.DB, "alice")
	T.saveProject(udb, Project{ID: "p1", Name: "P", Kind: KindClone, Owner: "alice", Source: sourceBundle, BundleName: "p.bundle"})
	w := raw("alice", "POST", "/api/projects", []byte(`{"id":"p1","name":"P","kind":"clone","collections":["api-docs","bobs","standards"]}`), "application/json")
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var p Project
	udb.Get(projectTable, "p1", &p)
	if strings.Join(p.Collections, ",") != "api-docs,standards" {
		t.Errorf("kept %q", p.Collections)
	}
}

// Deleting a file asks, and "always" is remembered for the project, as an
// approval is in Claude Code.
func TestADeleteCanBeAllowedAlways(t *testing.T) {
	c := &toolCtx{project: Project{ID: "p", Name: "P", Kind: KindClone}}
	for _, d := range c.codeTools() {
		if d.Tool.Name == "delete_file" {
			if !d.NeedsConfirm || d.Confirmation == nil || !d.Confirmation.CanRemember() {
				t.Errorf("delete_file: asks %v, remembers %v", d.NeedsConfirm, d.Confirmation != nil && d.Confirmation.CanRemember())
			}
			return
		}
	}
	t.Fatal("no delete_file tool")
}
