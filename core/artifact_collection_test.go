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
	if err != nil || !strings.Contains(skip, "collection with this name") {
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

// collectionTestUsers registers users on a fresh auth store, which is what the
// import's id-in-use probe and ListArtifacts enumerate.
func collectionTestUsers(t *testing.T, users ...string) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	for _, u := range users {
		adb.Set(AuthTable, "user:"+u, AuthUser{Username: u})
	}
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
}

// A name is an address among the owner's OWN collections. Carol's shared
// "Legal" and the deployment's "Handbook" are readable by alice, and by-name
// export used to ship whichever was touched last under alice's bundle.
func TestCollectionExportByNameIsOwnOnly(t *testing.T) {
	collectionTestDB(t)
	SaveCollection(UserDB(CollectionsDB(), "carol"), Collection{
		ID: "carol-legal", Owner: "carol", Name: "Legal", AllowedUsers: []string{"alice"},
	})
	RootDB.Set(GlobalCollectionsTable, "dep-hb", Collection{ID: "dep-hb", Name: "Handbook", Scope: CollectionScopeDeployment})
	if _, ok := LoadCollection(UserDB(CollectionsDB(), "alice"), "alice", "carol-legal"); !ok {
		t.Fatal("setup: the shared collection should be readable by alice")
	}
	for _, name := range []string{"Legal", "legal", "Handbook"} {
		if _, err := (collectionArtifact{}).ExportArtifact(nil, name, "alice"); err == nil {
			t.Errorf("export of %q by name resolved to a collection alice does not own", name)
		}
	}
	// Her own "Legal" is the one a name means, even when carol's is newer.
	seedCollection(t, "alice", "alice-legal", "Legal")
	SaveCollection(UserDB(CollectionsDB(), "carol"), Collection{
		ID: "carol-legal", Owner: "carol", Name: "Legal", AllowedUsers: []string{"alice"},
	})
	recipe, err := collectionArtifact{}.ExportArtifact(nil, "Legal", "alice")
	if err != nil {
		t.Fatal(err)
	}
	var pc PortableCollection
	_ = json.Unmarshal(recipe, &pc)
	if pc.ID != "alice-legal" {
		t.Errorf("by-name export picked %q, want alice's own", pc.ID)
	}
	// Two of her own under one name is ambiguous: an error naming both, never
	// a silent pick.
	seedCollection(t, "alice", "alice-legal-2", "LEGAL")
	_, err = collectionArtifact{}.ExportArtifact(nil, "Legal", "alice")
	if err == nil || !strings.Contains(err.Error(), "alice-legal") || !strings.Contains(err.Error(), "alice-legal-2") {
		t.Errorf("duplicate own names must refuse, naming the ids: %v", err)
	}
	// An id is exact, so it still resolves (a skill attached to a shared
	// collection is satisfied by it).
	if _, err := (collectionArtifact{}).ExportArtifact(nil, "alice-legal-2", "alice"); err != nil {
		t.Errorf("by-id export: %v", err)
	}
}

// Two own collections sharing a name are selected by id, or "export all" and
// the account backup would carry one corpus twice (or now, fail as ambiguous).
func TestCollectionListArtifactsSelectsDuplicateNamesByID(t *testing.T) {
	collectionTestDB(t)
	collectionTestUsers(t, "alice")
	seedCollection(t, "alice", "c1", "Legal")
	seedCollection(t, "alice", "c2", "legal")
	seedCollection(t, "alice", "c3", "Runbooks")
	got := map[string]bool{}
	for _, s := range (collectionArtifact{}).ListArtifacts(nil) {
		got[s.Name] = true
	}
	for _, want := range []string{"c1", "c2", "Runbooks"} {
		if !got[want] {
			t.Errorf("selection %q missing from %v", want, got)
		}
	}
	b, err := ExportArtifactBundleShallow(RootDB, (collectionArtifact{}).ListArtifacts(nil))
	if err != nil {
		t.Fatalf("export all: %v", err)
	}
	ids := map[string]bool{}
	for _, a := range b.Artifacts {
		var pc PortableCollection
		_ = json.Unmarshal(a.Recipe, &pc)
		ids[pc.ID] = true
	}
	if len(ids) != 3 {
		t.Errorf("export all must carry each collection once, got %v", ids)
	}
}

// Being shared a colleague's "Legal" (or the deployment carrying one) must not
// block importing a "Legal" of your own. Only your own name collides.
func TestCollectionImportNameCollidesWithOwnOnly(t *testing.T) {
	collectionTestDB(t)
	SaveCollection(UserDB(CollectionsDB(), "carol"), Collection{
		ID: "carol-legal", Owner: "carol", Name: "Legal", AllowedUsers: []string{"alice"},
	})
	RootDB.Set(GlobalCollectionsTable, "dep-hb", Collection{ID: "dep-hb", Name: "Handbook", Scope: CollectionScopeDeployment})
	for _, name := range []string{"Legal", "Handbook"} {
		recipe, _ := json.Marshal(PortableCollection{ID: "new-" + name, Name: name})
		_, skip, err := collectionArtifact{}.ImportArtifact(nil, recipe, "alice")
		if err != nil || skip != "" {
			t.Fatalf("import %q was blocked by somebody else's collection: skip=%q err=%v", name, skip, err)
		}
	}
	recipe, _ := json.Marshal(PortableCollection{ID: "another", Name: "legal"})
	_, skip, err := collectionArtifact{}.ImportArtifact(nil, recipe, "alice")
	if err != nil || !strings.Contains(skip, "already have a collection with this name") {
		t.Fatalf("her own name must still collide: skip=%q err=%v", skip, err)
	}
	// A traveled id she can already read skips, and says whose it is.
	shared, _ := json.Marshal(PortableCollection{ID: "carol-legal", Name: "Something"})
	_, skip, _ = collectionArtifact{}.ImportArtifact(nil, shared, "alice")
	if !strings.Contains(skip, "shared with you by carol") {
		t.Errorf("a skip onto a colleague's collection must say so: %q", skip)
	}
}

// When a traveled collection id is somebody else's here, the import re-mints
// it, and the bundle's skill must follow to the new id in either import order.
// Left on the old id it searched nothing, and the day that collection's owner
// shared it with the importer, it would have searched THEIR corpus instead.
func TestCollectionRemintRepointsTheBundlesSkill(t *testing.T) {
	for _, collectionFirst := range []bool{false, true} {
		collectionTestDB(t)
		collectionTestUsers(t, "alice", "bob")
		seedCollection(t, "bob", "bobs-coll", "Bob's notes")

		skill, _ := json.Marshal(SkillRecord{Name: "law", Description: "Use for case law.", AttachedCollections: []string{"bobs-coll"}})
		coll, _ := json.Marshal(PortableCollection{ID: "bobs-coll", Name: "Case Law"})
		arts := []PortableArtifact{{Type: "skill", Name: "law", Recipe: skill}, {Type: "collection", Name: "bobs-coll", Recipe: coll}}
		if collectionFirst {
			arts[0], arts[1] = arts[1], arts[0]
		}
		data, _ := json.Marshal(ArtifactBundle{Bundle: ArtifactBundleFormat, Artifacts: arts})
		res, err := ImportArtifactBundleAsUser(RootDB, data, "alice")
		if err != nil || res.Imported != 2 {
			t.Fatalf("collectionFirst=%v: import: %+v err=%v", collectionFirst, res, err)
		}
		var mine Collection
		for _, c := range ListCollections(UserDB(CollectionsDB(), "alice"), "alice") {
			if c.Name == "Case Law" {
				mine = c
			}
		}
		if mine.ID == "" || mine.ID == "bobs-coll" {
			t.Fatalf("collectionFirst=%v: expected a re-minted import, got %+v", collectionFirst, mine)
		}
		s, ok := FindSkillByName(RootDB, "alice", "law")
		if !ok {
			t.Fatalf("collectionFirst=%v: skill did not land", collectionFirst)
		}
		if len(s.AttachedCollections) != 1 || s.AttachedCollections[0] != mine.ID {
			t.Errorf("collectionFirst=%v: skill attaches %v, want the imported copy %s", collectionFirst, s.AttachedCollections, mine.ID)
		}
		if len(res.Warnings) != 0 {
			t.Errorf("collectionFirst=%v: a re-pointed skill has nothing missing: %v", collectionFirst, res.Warnings)
		}
	}
}
