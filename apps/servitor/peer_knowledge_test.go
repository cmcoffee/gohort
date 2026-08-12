package servitor

import (
	"path/filepath"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// TestWriteDocAtKeepsTheOriginTimestamp — the single most important property of
// a knowledge pull. Every staleness signal in this app is downstream of Updated
// meaning "when this was LEARNED". Stamp it with the copy time and a June map
// reads as today's, and the lead states it as current.
func TestWriteDocAtKeepsTheOriginTimestamp(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	origin := "2026-06-03T09:15:00Z"
	writeDocAt(db, "app-1", "overview", "nginx fronts the app", origin)

	var entry KnowledgeDocEntry
	if !db.Get(knowledgeTable, "app-1:overview", &entry) {
		t.Fatal("document was not stored")
	}
	if entry.Updated != origin {
		t.Errorf("Updated = %q, want the origin's %q — a copied doc must not look freshly learned", entry.Updated, origin)
	}

	// And it must read back as OLD, which is what the prompts act on.
	_, age := readDocWithAge(db, "app-1", "overview", time.Now())
	if age == "" {
		t.Error("no age rendered for a pulled doc")
	}
	if !docIsStale(entry.Updated, time.Now()) {
		t.Error("a doc learned in June is not being reported stale — the staleness banner would never fire on pulled knowledge")
	}
}

// TestWriteDocAtFallsBackWhenTheOriginGaveNoTime — a peer running an older
// build may send no timestamp. Better to stamp now than to store an empty one,
// which renders as no age at all and reads as "recent".
func TestWriteDocAtFallsBackWhenTheOriginGaveNoTime(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	writeDocAt(db, "app-1", "services", "postgres 16", "  ")
	var entry KnowledgeDocEntry
	db.Get(knowledgeTable, "app-1:services", &entry)
	if entry.Updated == "" {
		t.Error("an empty origin timestamp was stored as empty — the doc would render with no age")
	}
}

// TestWriteDocStillStampsNow — the ordinary path is unchanged.
func TestWriteDocStillStampsNow(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	writeDoc(db, "app-1", "apps", "the thing")
	var entry KnowledgeDocEntry
	db.Get(knowledgeTable, "app-1:apps", &entry)
	got, perr := time.Parse(time.RFC3339, entry.Updated)
	if perr != nil {
		t.Fatalf("unparseable timestamp %q", entry.Updated)
	}
	if time.Since(got) > time.Minute {
		t.Errorf("writeDoc stamped %v, not now", got)
	}
}

// TestPullPeerKnowledgeRefusesANonStub — pulling into a local appliance would
// overwrite knowledge this instance gathered itself.
func TestPullPeerKnowledgeRefusesANonStub(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	app := &Servitor{}
	if _, _, err := app.pullPeerKnowledge(nil, db, Appliance{ID: "a", Type: "ssh"}); err == nil {
		t.Error("pulled knowledge into a local appliance — it would clobber what was gathered here")
	}
}

// TestNewerHereDecidesTheMerge — the whole backfill rests on this. Replacement
// was safe while a stub was passive; now that this instance investigates the
// same machine, a wholesale pull would delete what it just learned.
func TestNewerHereDecidesTheMerge(t *testing.T) {
	older, newer := "2026-06-01T00:00:00Z", "2026-08-01T00:00:00Z"

	if !newerHere(newer, older) {
		t.Error("a locally-newer item should be kept")
	}
	if newerHere(older, newer) {
		t.Error("a locally-older item should be overwritten by the incoming one")
	}
	// No local timestamp: the incoming side at least says when it learned the
	// thing, so it wins rather than an unknown age blocking the backfill.
	if newerHere("", newer) {
		t.Error("a local item with no timestamp should not beat a dated incoming one")
	}
	if newerHere("nonsense", newer) {
		t.Error("an unparseable local timestamp should not win")
	}
	// Incoming undated but local dated: keep local, which is the known quantity.
	if !newerHere(newer, "") {
		t.Error("a dated local item should beat an undated incoming one")
	}
}

// TestPullMergeKeepsLocallyNewerDocs — the failure this guards is silent: the
// pull reports success and the newer local content is simply gone.
func TestPullMergeKeepsLocallyNewerDocs(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Learned here five minutes ago.
	local := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	writeDocAt(db, "app-1", "overview", "what THIS instance found", local)

	if newerHere(docUpdatedAt(db, "app-1", "overview"), "2026-06-01T00:00:00Z") != true {
		t.Fatal("a doc written five minutes ago did not beat a June one")
	}
	// And the reverse: an older local doc yields to a fresher remote one.
	writeDocAt(db, "app-1", "services", "stale local", "2026-01-01T00:00:00Z")
	if newerHere(docUpdatedAt(db, "app-1", "services"), "2026-07-01T00:00:00Z") {
		t.Error("a January local doc should yield to a July remote one")
	}
}

// TestDocUpdatedAtOnAMissingDoc — an absent doc must report no timestamp, not a
// zero one that would read as very old and always lose.
func TestDocUpdatedAtOnAMissingDoc(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	if got := docUpdatedAt(db, "app-1", "nothing-here"); got != "" {
		t.Errorf("missing doc reported %q", got)
	}
	if docUpdatedAt(nil, "a", "b") != "" {
		t.Error("a nil store should report no timestamp rather than panic")
	}
}
