package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestApplyFactListEditDiffsInsteadOfWiping pins the memory editor's Save
// semantics: an untouched row keeps its identity and provenance (the old
// wipe+restore promoted model-authored notes to user_stated — into the
// grounding corpus — and reset their freshness), a removed row is deleted,
// and only genuinely new lines are stored as user_stated.
func TestApplyFactListEditDiffsInsteadOfWiping(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ns := factsNamespace("ag")
	kept := StoreMemoryFactP(db, ns, "model observed the deploy uses JWT auth", FactWritePolicy{Source: MemSourceObserved})
	removed := StoreMemoryFactP(db, ns, "user said hello once", FactWritePolicy{Source: MemSourceObserved})
	if kept.Reason != FactStored || removed.Reason != FactStored {
		t.Fatal("seeding failed")
	}

	// Editor round-trips the kept note verbatim, drops the other, adds one.
	applyFactListEdit(db, ns, []string{
		"model observed the deploy uses JWT auth",
		"team standup is at 09:30",
	}, nil)

	live := ListMemoryFacts(db, ns)
	if len(live) != 2 {
		t.Fatalf("expected 2 live facts, got %d: %+v", len(live), live)
	}
	byNote := map[string]MemoryFact{}
	for _, f := range live {
		byNote[f.Note] = f
	}
	got, ok := byNote["model observed the deploy uses JWT auth"]
	if !ok {
		t.Fatal("kept note missing")
	}
	if got.ID != kept.Fact.ID {
		t.Fatalf("kept note was re-stored (ID changed %s -> %s)", kept.Fact.ID, got.ID)
	}
	if got.Source != MemSourceObserved {
		t.Fatalf("kept note's Source was escalated: got %d, want observed", got.Source)
	}
	added, ok := byNote["team standup is at 09:30"]
	if !ok {
		t.Fatal("new note missing")
	}
	if added.Source != MemSourceUserStated {
		t.Fatalf("new manual note should be user_stated, got %d", added.Source)
	}
	if _, ok := GetMemoryFactByID(db, ns, removed.Fact.ID); ok {
		t.Fatal("removed note survived the edit")
	}
}

// TestExplainRetiredHoleMultiwordQuery: a natural-language question must
// surface a relevant tombstone. The old implementation required the FULL
// query to appear as a substring of the note, so it effectively never fired.
func TestExplainRetiredHoleMultiwordQuery(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ns := factsNamespace("ag")
	now := time.Now()
	db.Set(MemoryFactsTable, ns+"/job", MemoryFact{
		Namespace: ns, ID: "job", Note: "works at Acme Corp", Created: now,
		MemoryProvenance: MemoryProvenance{Reason: RetireSuperseded, RetiredAt: now},
	})
	out := explainRetiredHole(db, ns, "where does the user work")
	if out == "" {
		t.Fatal("expected the retired note to surface for a multi-word question")
	}
	if !strings.Contains(out, "Acme Corp") || !strings.Contains(out, "superseded") {
		t.Fatalf("hole explanation missing note or reason: %q", out)
	}
}

// TestDropAgentSideDataWipesFactsAndGraph: deleting an agent must take its
// fact namespace (live + tombstones) and its entity graph with it — the
// orphan the audit found meant recreating an agent under the same ID
// resurrected the old one's memory.
func TestDropAgentSideDataWipesFactsAndGraph(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ns := factsNamespace("ag1")
	if res := StoreMemoryFactP(db, ns, "user prefers metric units", FactWritePolicy{}); res.Reason != FactStored {
		t.Fatal("seeding fact failed")
	}
	UpsertGraphEntity(db, ns, "person", "Robin", nil, nil)
	LinkGraphEdge(db, ns, "person:robin", "works_at", "org:acme", "", false)

	dropAgentSideData(db, "u", "ag1")

	if got := ListMemoryFacts(db, ns); len(got) != 0 {
		t.Fatalf("facts survived agent delete: %+v", got)
	}
	if got := ListRetiredFacts(db, ns); len(got) != 0 {
		t.Fatalf("tombstones survived agent delete: %+v", got)
	}
	if e, ed := GraphCounts(db, ns); e != 0 || ed != 0 {
		t.Fatalf("graph survived agent delete: %d entities, %d edges", e, ed)
	}
}
