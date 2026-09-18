package orchestrate

// The curator's job is the part autofill has no concept of: an edit that makes
// the local copy wrong, and a deletion that leaves it a ghost still answering
// questions.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// fakeSource is a reference source that can enumerate, standing in for a wiki
// space. bodies and docs are swapped between syncs to stage an edit, a
// deletion or an outage.
type fakeSource struct {
	docs    []ReferenceDoc
	bodies  map[string]string
	listErr error
	bodyErr map[string]error
	fetched []string // doc ids whose body was actually pulled
}

func (f *fakeSource) Kind() string  { return "fake" }
func (f *fakeSource) Label() string { return "Fake wiki" }
func (f *fakeSource) List(string) []ReferenceItem {
	return []ReferenceItem{{ID: "space", Name: "Space"}}
}
func (f *fakeSource) Fetch(context.Context, string, string, string) string { return "" }

func (f *fakeSource) Documents(context.Context, string, string) ([]ReferenceDoc, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.docs, nil
}

func (f *fakeSource) DocumentBody(_ context.Context, _, _, docID string) (string, error) {
	if err := f.bodyErr[docID]; err != nil {
		return "", err
	}
	f.fetched = append(f.fetched, docID)
	return f.bodies[docID], nil
}

func curatorFixture(t *testing.T) (Database, Database, Collection, *fakeSource) {
	t.Helper()
	udb := &DBase{Store: kvlite.MemStore()}
	chunkDB := &DBase{Store: kvlite.MemStore()}
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks"}
	src := &fakeSource{
		docs: []ReferenceDoc{
			{ID: "a", Title: "Restarting the gateway", Version: "1"},
			{ID: "b", Title: "Rotating a key", Version: "1"},
		},
		bodies: map[string]string{
			"a": "Stop the service, wait for drain, start it again.",
			"b": "Generate the new key, publish it, retire the old one.",
		},
	}
	return udb, chunkDB, c, src
}

func runSync(t *testing.T, udb, chunkDB Database, c Collection, src *fakeSource) curateResult {
	t.Helper()
	res, err := curateCollection(context.Background(), udb, chunkDB, curateSpec{
		User: "alice", Collection: c, Source: src, Item: "space",
	})
	if err != nil {
		t.Fatalf("curate: %v", err)
	}
	return res
}

func TestCuratorAddsThenLeavesUnchangedDocumentsAlone(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)

	res := runSync(t, udb, chunkDB, c, src)
	if res.Added != 2 || res.Updated != 0 || res.Removed != 0 {
		t.Fatalf("first sync = %+v", res)
	}

	// A second sync over an unchanged source must not fetch a single body.
	// This is what keeps a scheduled sync over a large space affordable.
	src.fetched = nil
	res = runSync(t, udb, chunkDB, c, src)
	if res.Unchanged != 2 || res.Added != 0 || res.Updated != 0 {
		t.Fatalf("second sync = %+v", res)
	}
	if len(src.fetched) != 0 {
		t.Errorf("re-pulled %v when nothing had changed", src.fetched)
	}
}

func TestCuratorRepullsAnEditedDocument(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)
	runSync(t, udb, chunkDB, c, src)

	src.docs[0].Version = "2"
	src.bodies["a"] = "Drain first, THEN stop the service."
	src.fetched = nil

	res := runSync(t, udb, chunkDB, c, src)
	if res.Updated != 1 || res.Unchanged != 1 || res.Added != 0 {
		t.Fatalf("after an edit = %+v", res)
	}
	if len(src.fetched) != 1 || src.fetched[0] != "a" {
		t.Errorf("fetched %v, want only the edited document", src.fetched)
	}
	// Replaced, not stacked: the ingest deletes by reportID first, so the old
	// wording must not still be searchable beside the new.
	if n := countReportChunks(chunkDB, curatedReportID(c.ID, "fake", "space", "a")); n == 0 {
		t.Error("the edited document has no chunks")
	}
	found := false
	for _, ch := range allChunks(chunkDB) {
		if strings.Contains(ch, "Stop the service, wait for drain") {
			found = true
		}
	}
	if found {
		t.Error("the superseded wording is still in the collection")
	}
}

// The event autofill has no concept of. A deleted document leaves a copy that
// still answers questions, which is worse than a missing one.
func TestCuratorRetiresADeletedDocument(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)
	runSync(t, udb, chunkDB, c, src)
	gone := curatedReportID(c.ID, "fake", "space", "b")
	if countReportChunks(chunkDB, gone) == 0 {
		t.Fatal("setup: the document was never ingested")
	}

	src.docs = src.docs[:1] // "b" is deleted at the source

	res := runSync(t, udb, chunkDB, c, src)
	if res.Removed != 1 || res.Unchanged != 1 {
		t.Fatalf("after a deletion = %+v", res)
	}
	if n := countReportChunks(chunkDB, gone); n != 0 {
		t.Errorf("the retired document still has %d chunk(s)", n)
	}
	l := loadCuratedLedger(udb, c.ID, "fake", "space")
	if _, still := l.Docs["b"]; still {
		t.Error("the ledger still names the retired document")
	}
}

// A source whose auth quietly expired and one that was genuinely emptied look
// identical from here, and only one of those should cost somebody their copy.
func TestCuratorRefusesToEmptyACollectionOnAnEmptyAnswer(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)
	runSync(t, udb, chunkDB, c, src)

	src.docs = nil
	_, err := curateCollection(context.Background(), udb, chunkDB, curateSpec{
		User: "alice", Collection: c, Source: src, Item: "space",
	})
	if err == nil {
		t.Fatal("an empty enumeration wiped the collection")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error = %v", err)
	}
	if n := countReportChunks(chunkDB, curatedReportID(c.ID, "fake", "space", "a")); n == 0 {
		t.Error("the copy was deleted anyway")
	}
	if len(loadCuratedLedger(udb, c.ID, "fake", "space").Docs) != 2 {
		t.Error("the ledger was emptied")
	}
}

// A listing that fails is not a listing that is empty. Nothing is retired.
func TestCuratorReportsAFailedListingWithoutTouchingAnything(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)
	runSync(t, udb, chunkDB, c, src)

	src.listErr = errors.New("401 unauthorized")
	_, err := curateCollection(context.Background(), udb, chunkDB, curateSpec{
		User: "alice", Collection: c, Source: src, Item: "space",
	})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want the source's own reason", err)
	}
	if len(loadCuratedLedger(udb, c.ID, "fake", "space").Docs) != 2 {
		t.Error("a failed listing changed the ledger")
	}
}

// One document failing must not stop the others, and must not retire itself.
func TestCuratorCarriesOnPastOneFailedDocument(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)
	runSync(t, udb, chunkDB, c, src)

	src.docs[0].Version = "2"
	src.docs[1].Version = "2"
	src.bodyErr = map[string]error{"a": errors.New("gateway timeout")}
	src.bodies["b"] = "Rotate quarterly now."

	res := runSync(t, udb, chunkDB, c, src)
	if res.Failed != 1 || res.Updated != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "gateway timeout") {
		t.Errorf("notes = %v — a failure must say why", res.Notes)
	}
	// The failed document keeps the copy it had, and stays in the ledger at
	// its OLD version so the next sync tries it again.
	l := loadCuratedLedger(udb, c.ID, "fake", "space")
	if l.Docs["a"].Version != "1" {
		t.Errorf("failed document recorded as version %q", l.Docs["a"].Version)
	}
	if countReportChunks(chunkDB, curatedReportID(c.ID, "fake", "space", "a")) == 0 {
		t.Error("a failed pull dropped the copy that was already there")
	}
}

// A source with no version and no timestamp cannot say whether anything
// changed, so the honest behaviour is to re-pull rather than assume current.
func TestCuratorRepullsWhenASourceCannotSayWhatChanged(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)
	src.docs = []ReferenceDoc{{ID: "a", Title: "No version"}}
	src.bodies = map[string]string{"a": "first"}
	runSync(t, udb, chunkDB, c, src)

	src.fetched = nil
	res := runSync(t, udb, chunkDB, c, src)
	if res.Unchanged != 0 || res.Updated != 1 {
		t.Fatalf("result = %+v — an unknowable version must re-pull", res)
	}
	if len(src.fetched) != 1 {
		t.Errorf("fetched %v", src.fetched)
	}
}

// A timestamp is the fallback marker when a source has no version of its own.
func TestCuratorUsesUpdatedWhenThereIsNoVersion(t *testing.T) {
	udb, chunkDB, c, src := curatorFixture(t)
	at := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	src.docs = []ReferenceDoc{{ID: "a", Title: "Timestamped", Updated: at}}
	src.bodies = map[string]string{"a": "first"}
	runSync(t, udb, chunkDB, c, src)

	res := runSync(t, udb, chunkDB, c, src)
	if res.Unchanged != 1 {
		t.Fatalf("same timestamp = %+v, want unchanged", res)
	}
	src.docs[0].Updated = at.Add(time.Hour)
	res = runSync(t, udb, chunkDB, c, src)
	if res.Updated != 1 {
		t.Fatalf("newer timestamp = %+v, want a re-pull", res)
	}
}

// A source that cannot enumerate says so in terms of what to do instead.
func TestCuratorRefusesASourceThatCannotList(t *testing.T) {
	udb, chunkDB, c, _ := curatorFixture(t)
	_, err := curateCollection(context.Background(), udb, chunkDB, curateSpec{
		User: "alice", Collection: c, Source: searchOnlySource{}, Item: "space",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot list") {
		t.Fatalf("err = %v", err)
	}
}

type searchOnlySource struct{}

func (searchOnlySource) Kind() string                                         { return "search-only" }
func (searchOnlySource) Label() string                                        { return "Search only" }
func (searchOnlySource) List(string) []ReferenceItem                          { return nil }
func (searchOnlySource) Fetch(context.Context, string, string, string) string { return "" }

// allChunks returns every chunk body in the store, for asserting that
// superseded text is really gone.
func allChunks(db Database) []string {
	var out []string
	for _, c := range ChunksWhere(db, func(EmbeddedChunk) bool { return true }) {
		out = append(out, c.Text)
	}
	return out
}

// A collection bound to a source that is no longer connected must say so. The
// one thing it must not look like is a sync that found nothing to do.
func TestCurateAllReportsADisconnectedSource(t *testing.T) {
	app, _, udb := authedApp(t)
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		CuratedFrom: []CuratedSource{{Kind: "gone", Item: "space", Label: "Old wiki"}}}
	saveCollection(udb, c)

	lines := app.curateCollectionAll(context.Background(), "alice", c)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], "not connected") || !strings.Contains(lines[0], "Old wiki") {
		t.Errorf("line = %q", lines[0])
	}
	// And the copy is untouched: a source that is gone is not a source that
	// deleted everything.
	if len(loadCuratedLedger(udb, c.ID, "gone", "space").Docs) != 0 {
		t.Error("a disconnected source wrote a ledger")
	}
}

// Each binding is its own sync: one failing must not stop the next.
func TestCurateAllKeepsGoingAfterOneBindingFails(t *testing.T) {
	app, _, udb := authedApp(t)
	// collectionDB hands back the global vector store, so a test that ingests
	// through the app has to give it one or every ingest silently indexes
	// nothing and reads as a failed pull.
	prev := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { VectorDB = prev })

	good := &fakeSource{
		docs:   []ReferenceDoc{{ID: "a", Title: "Kept", Version: "1"}},
		bodies: map[string]string{"a": "body"},
	}
	RegisterReferenceSource(good)

	c := Collection{ID: "col-2", Owner: "alice", Name: "Mixed", CuratedFrom: []CuratedSource{
		{Kind: "missing", Item: "x", Label: "Missing one"},
		{Kind: "fake", Item: "space", Label: "Working one"},
	}}
	saveCollection(udb, c)

	lines := app.curateCollectionAll(context.Background(), "alice", c)
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], "not connected") {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "1 added") {
		t.Errorf("second line = %q — the working binding must still run", lines[1])
	}
}
