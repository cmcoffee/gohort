package servitor

import (
	"path/filepath"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// TestWriteDocAtKeepsTheGivenTimestamp: every staleness signal in this app is
// downstream of Updated meaning "when this was LEARNED". A doc written with an
// older time must read back as that old, or a June map reads as today's and
// the lead states it as current.
func TestWriteDocAtKeepsTheGivenTimestamp(t *testing.T) {
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
		t.Errorf("Updated = %q, want the origin's %q — an old doc must not look freshly learned", entry.Updated, origin)
	}

	// And it must read back as OLD, which is what the prompts act on.
	_, age := readDocWithAge(db, "app-1", "overview", time.Now())
	if age == "" {
		t.Error("no age rendered for an old doc")
	}
	if !docIsStale(entry.Updated, time.Now()) {
		t.Error("a doc learned in June is not being reported stale: the staleness banner would never fire")
	}
}

// TestWriteDocAtFallsBackWhenGivenNoTime: better to stamp now than to store an
// empty one, which renders as no age at all and reads as "recent".
func TestWriteDocAtFallsBackWhenGivenNoTime(t *testing.T) {
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
