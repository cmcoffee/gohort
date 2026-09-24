package orchestrate

// forget(query=...) used to delete every finding above the search relevance
// floor. Asked to "clear those pending edits", it deleted a note about a GPU
// setup (0.45) and one about a trip (0.40), while the edits sat in a pinned
// note it never searched. Query mode now lists candidates from both places and
// deletes nothing; the delete is by id.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestForgetByQueryDeletesNothingAndOffersBothKinds(t *testing.T) {
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })

	db := &DBase{Store: kvlite.MemStore()}
	ct := driftTurn(db)
	ct.app = &OrchestrateApp{}
	ct.app.DB = db
	ns := factsNamespace("ag")
	note, _, _ := StoreMemoryFact(db, ns, "Pending edits for the Shazz photo: AZBLST plate, Big Sur background")
	own := knowledgeSource("u", "ag", "general")
	VectorDB.Set(EmbeddedChunks, "c1", EmbeddedChunk{ID: "c1", Source: own, ReportID: "r-gpu", Section: "## GPU setup", Text: "Pending photo edits workflow runs on the second GPU"})
	VectorDB.Set(EmbeddedChunks, "c2", EmbeddedChunk{ID: "c2", Source: own, ReportID: "r-trip", Section: "## Trip", Text: "Back from a trip, photos to edit later"})
	InvalidateChunkCache()

	forget := ct.forgetToolDef().Handler
	out, err := forget(context.Background(), map[string]any{"query": "pending photo edits Shazz"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing has been deleted") || !strings.Contains(out, "fact:"+note.ID) {
		t.Fatalf("the preview must delete nothing and offer the pinned note:\n%s", out)
	}
	var c EmbeddedChunk
	if !VectorDB.Get(EmbeddedChunks, "c1", &c) || !VectorDB.Get(EmbeddedChunks, "c2", &c) {
		t.Fatal("a query deleted findings")
	}
	if len(ListMemoryFacts(db, ns)) != 1 {
		t.Fatal("a query deleted the pinned note")
	}

	// The id it just offered is deletable in the same turn, though the saved
	// conversation has not seen the tool result yet.
	msg, err := forget(context.Background(), map[string]any{"id": "fact:" + note.ID})
	// A pending note is an open item, which forget closes rather than
	// deletes; either way it leaves the saved notes this turn.
	if err != nil || !(strings.Contains(msg, "Forgot") || strings.Contains(msg, "Closed the open item")) {
		t.Fatalf("the offered id should delete: %q %v", msg, err)
	}
	if len(ListMemoryFacts(db, ns)) != 0 {
		t.Error("the pinned note survived its delete")
	}
	if !VectorDB.Get(EmbeddedChunks, "c1", &c) || !VectorDB.Get(EmbeddedChunks, "c2", &c) {
		t.Error("deleting the note took findings with it")
	}
}

func TestForgetByQueryWithNoMatchSaysSo(t *testing.T) {
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })
	db := &DBase{Store: kvlite.MemStore()}
	ct := driftTurn(db)
	ct.app = &OrchestrateApp{}
	ct.app.DB = db
	out, err := ct.forgetToolDef().Handler(context.Background(), map[string]any{"query": "zeppelin timetable"})
	if err != nil || !strings.Contains(out, "nothing matched") {
		t.Errorf("an empty preview must say nothing matched: %q %v", out, err)
	}
}
