package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func newMemSearchFixture(t *testing.T) (*OrchestrateApp, Database, AgentRecord) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	udb := UserDB(root, "u")
	rec, err := saveAgent(udb, AgentRecord{Name: "Molt", Owner: "u", Cortex: true, EnableNotes: true, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	return app, udb, rec
}

// TestMemSearchGrepFindsAndDeletes pins the owner search across the
// enumerable layers: a pinned fact, a cortex observation, and the Working
// notes block are all findable by substring, and each delete path removes
// exactly the addressed item.
func TestMemSearchGrepFindsAndDeletes(t *testing.T) {
	app, udb, rec := newMemSearchFixture(t)
	ns := factsNamespace(rec.ID)
	fact, _, _ := StoreMemoryFact(udb, ns, "mark_notifications_read is permanently broken")
	appendCortexObs(udb, rec.ID, "Session", cortexKindSession, "Cycle 43: mark_notifications_read still broken, 38 attempts")
	SaveOperatingNotes(udb, ns, "workaround: use fetch_url against the raw API since mark_notifications_read is broken")

	items := app.memSearchGrep("u", udb, rec, "mark_notifications_read")
	byLayer := map[string]memSearchItem{}
	for _, it := range items {
		byLayer[it.Layer] = it
	}
	for _, layer := range []string{"pinned", "cortex", "notes"} {
		if _, ok := byLayer[layer]; !ok {
			t.Fatalf("grep should surface the %s hit; got layers %v", layer, byLayer)
		}
	}
	if !byLayer["pinned"].Deletable || byLayer["pinned"].ID != "fact:"+fact.ID {
		t.Fatalf("pinned hit should be deletable with a fact: id; got %+v", byLayer["pinned"])
	}

	// Cortex delete tombstones in place (no message-index shift for the
	// compaction cursor) and the item stops matching.
	cortexRef := strings.TrimPrefix(byLayer["cortex"].ID, "cortex:")
	if !tombstoneCortexObservation(udb, rec.ID, cortexRef) {
		t.Fatal("tombstoneCortexObservation should succeed for a live observation")
	}
	sess, _ := loadChatSession(udb, rec.ID, cortexSessionID(rec.ID))
	if got := len(sess.Messages); got != 1 {
		t.Fatalf("tombstone must not remove the message (index stability); got %d messages", got)
	}
	if again := app.memSearchGrep("u", udb, rec, "mark_notifications_read"); func() bool {
		for _, it := range again {
			if it.Layer == "cortex" {
				return true
			}
		}
		return false
	}() {
		t.Fatal("tombstoned cortex observation still matches the search")
	}
}

// TestParseRecallRendered pins the parse of recall's rendered block back
// into structured items — layer tags, ids, deletability by kind, and the
// pinned-note special case (content rides the bullet line, not a snippet).
func TestParseRecallRendered(t *testing.T) {
	rendered := "- [pinned] use fetch_url for the raw API\n  id: fact:abc123\n\n" +
		"- [finding] Moltbook API behaviors\n  id: mem:rep-1\n  the endpoint doubles its path sometimes\n  (saved 2026-07-20)\n\n" +
		"- [knowledge] Post to Moltbook via REST\n  id: doc:rep-2\n  curl the public endpoint\n\n" +
		"- [history] operator history — messages 0–13\n  id: span:sp-9\n  fetch_url storm transcript"
	items := parseRecallRendered(rendered)
	if len(items) != 4 {
		t.Fatalf("want 4 items, got %d: %+v", len(items), items)
	}
	want := []struct {
		layer, id string
		deletable bool
	}{
		{"pinned", "fact:abc123", true},
		{"finding", "mem:rep-1", true},
		{"knowledge", "doc:rep-2", true},
		{"history", "span:sp-9", false},
	}
	for i, w := range want {
		if items[i].Layer != w.layer || items[i].ID != w.id || items[i].Deletable != w.deletable {
			t.Fatalf("item %d: want %+v, got %+v", i, w, items[i])
		}
	}
	if items[0].Text != "use fetch_url for the raw API" {
		t.Fatalf("pinned content should move from title to text; got %+v", items[0])
	}
	if !strings.Contains(items[1].Text, "doubles its path") {
		t.Fatalf("finding snippet lost: %+v", items[1])
	}
}
