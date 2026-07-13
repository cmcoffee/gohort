package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// collectionTestDB pins RootDB and VectorDB to fresh in-memory stores for the
// test's duration — CollectionsDB() derives from RootDB and chunk I/O routes
// to VectorDB, so both must point at stores the test controls. The chunk
// cache is invalidated on the way in and out so no snapshot leaks across
// tests that reuse the globals.
func collectionTestDB(t *testing.T) {
	t.Helper()
	savedRoot, savedVec := RootDB, VectorDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() {
		RootDB, VectorDB = savedRoot, savedVec
		InvalidateChunkCache()
	})
}

func seedCollection(t *testing.T, owner, id, name string) Collection {
	t.Helper()
	udb := UserDB(CollectionsDB(), owner)
	if udb == nil {
		t.Fatal("no collections store")
	}
	c := Collection{ID: id, Owner: owner, Name: name, Description: "d"}
	SaveCollection(udb, c)
	return c
}

func TestCollectionArtifact_ExportCarriesTextNotVectors(t *testing.T) {
	collectionTestDB(t)
	c := seedCollection(t, "alice", "coll-1", "Runbooks")
	src := CollectionSource(c.ID)
	rows := []EmbeddedChunk{
		{ID: "ch-1", Source: src, ReportID: "doc-1", Title: "Doc", Section: "## A", Text: "alpha", Vector: []float32{0.1, 0.2}, Model: "m", Date: "2026-01-01", Locator: "page 1"},
		{ID: "ch-2", Source: src, ReportID: "doc-1", Title: "Doc", Section: "## B", Text: "beta", Vector: []float32{0.3, 0.4}, Model: "m"},
		{ID: "ch-x", Source: "collection:other", Text: "not ours", Vector: []float32{0.5}},
	}
	for _, r := range rows {
		VectorDB.Set(EmbeddedChunks, r.ID, r)
	}
	InvalidateChunkCache()

	// Resolves by display name AND by ID (cross-artifact references are IDs).
	for _, key := range []string{"Runbooks", "coll-1"} {
		recipe, err := collectionArtifact{}.ExportArtifact(nil, key, "alice")
		if err != nil {
			t.Fatalf("export by %q: %v", key, err)
		}
		var pc PortableCollection
		if err := json.Unmarshal(recipe, &pc); err != nil {
			t.Fatalf("recipe unmarshal: %v", err)
		}
		if pc.ID != "coll-1" {
			t.Fatalf("collection ID must travel (cross-artifact reference key), got %q", pc.ID)
		}
		if len(pc.Chunks) != 2 {
			t.Fatalf("expected this collection's 2 chunks, got %+v", pc.Chunks)
		}
		if pc.Chunks[0].Text != "alpha" || pc.Chunks[0].Locator != "page 1" || pc.Chunks[1].Text != "beta" {
			t.Fatalf("chunk text/shape must travel intact (sorted): %+v", pc.Chunks)
		}
		if strings.Contains(string(recipe), `"vector"`) || strings.Contains(string(recipe), `"model"`) {
			t.Fatalf("vectors/model must not travel: %s", recipe)
		}
	}

	if _, err := (collectionArtifact{}).ExportArtifact(nil, "Runbooks", ""); err == nil {
		t.Fatal("export without an owner must error")
	}
	if _, err := (collectionArtifact{}).ExportArtifact(nil, "nope", "alice"); err == nil {
		t.Fatal("export of a missing collection must error")
	}
}

func TestCollectionArtifact_ImportPreservesIDLandsUserScoped(t *testing.T) {
	collectionTestDB(t)
	recipe, _ := json.Marshal(PortableCollection{
		ID: "coll-77", Name: "K8s Docs", Description: "ref",
		FilterRules: "official only", IngestedURLs: []string{"https://x"},
	})

	name, skip, err := collectionArtifact{}.ImportArtifact(nil, recipe, "bob")
	if err != nil || skip != "" || name != "K8s Docs" {
		t.Fatalf("import: name=%q skip=%q err=%v", name, skip, err)
	}
	udb := UserDB(CollectionsDB(), "bob")
	got, ok := LoadCollection(udb, "bob", "coll-77")
	if !ok {
		t.Fatal("imported collection not found under its preserved ID")
	}
	if got.Owner != "bob" || IsDeploymentScope(got) {
		t.Fatalf("import must land user-scoped under the importer: %+v", got)
	}
	if got.FilterRules != "official only" || len(got.IngestedURLs) != 1 {
		t.Fatalf("metadata must travel: %+v", got)
	}

	// Same ID again → skip; same name under a fresh ID → skip too.
	_, skip, err = collectionArtifact{}.ImportArtifact(nil, recipe, "bob")
	if err != nil || !strings.Contains(skip, "id already exists") {
		t.Fatalf("same-id import must skip: skip=%q err=%v", skip, err)
	}
	renamedID, _ := json.Marshal(PortableCollection{ID: "coll-88", Name: "k8s docs"})
	_, skip, err = collectionArtifact{}.ImportArtifact(nil, renamedID, "bob")
	if err != nil || !strings.Contains(skip, "name already exists") {
		t.Fatalf("same-name import must skip: skip=%q err=%v", skip, err)
	}
}

func TestSkillClosure_CarriesAttachedCollection(t *testing.T) {
	// The chain this type exists for: exporting a skill folds in its attached
	// collection (corpus and all), and the preserved collection ID keeps the
	// skill's reference valid on the importing install. Runs against the REAL
	// registry, not fakes.
	collectionTestDB(t)
	c := seedCollection(t, "alice", "coll-ref", "Case Law")
	VectorDB.Set(EmbeddedChunks, "ch-1", EmbeddedChunk{
		ID: "ch-1", Source: CollectionSource(c.ID), ReportID: "d1", Text: "precedent", Vector: []float32{0.1}})
	InvalidateChunkCache()
	if _, err := SaveSkill(RootDB, "alice", SkillRecord{
		Name: "law", Description: "Use for case law.",
		AttachedCollections: []string{"coll-ref"},
	}); err != nil {
		t.Fatalf("save skill: %v", err)
	}

	b, err := ExportArtifactBundle(RootDB, []ArtifactSel{{Type: "skill", Name: "law", Owner: "alice"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(b.Artifacts) != 2 || b.Artifacts[0].Type != "skill" || b.Artifacts[1].Type != "collection" {
		t.Fatalf("expected [skill, collection], got %v", bundleNames(b))
	}
	var pc PortableCollection
	if err := json.Unmarshal(b.Artifacts[1].Recipe, &pc); err != nil {
		t.Fatalf("collection recipe: %v", err)
	}
	if pc.ID != "coll-ref" || len(pc.Chunks) != 1 || pc.Chunks[0].Text != "precedent" {
		t.Fatalf("collection must travel with preserved ID + corpus text: %+v", pc)
	}
}

func TestIngestImportedCollectionChunks_NoEmbeddingBackend(t *testing.T) {
	collectionTestDB(t)
	// No embedding backend configured in tests → chunks must still land,
	// text intact, without vectors (keyword search reaches them; a later
	// re-embed can fill the gap).
	ingestImportedCollectionChunks("coll-9", "Imported", []PortableChunk{
		{ReportID: "doc-1", Title: "T", Section: "## A", Text: "alpha", Date: "2026-01-01"},
		{Text: "   "}, // blank text is dropped, not stored
		{ReportID: "doc-1", Section: "## B", Text: "beta", Kind: "user_comment"},
	})
	got := ChunksForSource(VectorDB, CollectionSource("coll-9"))
	if len(got) != 2 {
		t.Fatalf("expected 2 stored chunks, got %+v", got)
	}
	for _, ch := range got {
		if len(ch.Vector) != 0 {
			t.Fatalf("no backend → no vector, got %+v", ch)
		}
		if ch.ID == "" || ch.Source != CollectionSource("coll-9") {
			t.Fatalf("chunks need fresh IDs + the collection's source tag: %+v", ch)
		}
		if ch.Date == "" {
			t.Fatalf("date must be kept or stamped: %+v", ch)
		}
	}
}
