package core

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func notesTestDB() Database { return &DBase{Store: kvlite.MemStore()} }

func TestOperatingNotesRewriteAndHistory(t *testing.T) {
	db := notesTestDB()
	ns := "agent:x"

	if n := LoadOperatingNotes(db, ns); n.Text != "" {
		t.Fatalf("expected empty, got %q", n.Text)
	}

	if _, over := SaveOperatingNotes(db, ns, "draft v1"); over {
		t.Fatalf("v1 unexpectedly over cap")
	}
	if _, over := SaveOperatingNotes(db, ns, "draft v2"); over {
		t.Fatalf("v2 over cap")
	}
	got := LoadOperatingNotes(db, ns)
	if got.Text != "draft v2" {
		t.Fatalf("rewrite did not replace: %q", got.Text)
	}
	if len(got.History) != 1 || got.History[0] != "draft v1" {
		t.Fatalf("history ring wrong: %v", got.History)
	}
	// History ring caps at operatingNotesHistoryDepth (3).
	for _, v := range []string{"v3", "v4", "v5", "v6"} {
		SaveOperatingNotes(db, ns, v)
	}
	if h := LoadOperatingNotes(db, ns).History; len(h) != operatingNotesHistoryDepth {
		t.Fatalf("history not capped at %d: %v", operatingNotesHistoryDepth, h)
	}
}

func TestOperatingNotesCapRejects(t *testing.T) {
	db := notesTestDB()
	ns := "agent:x"
	SaveOperatingNotes(db, ns, "keeper")

	huge := strings.Repeat("z", OperatingNotesCap+1)
	got, over := SaveOperatingNotes(db, ns, huge)
	if !over {
		t.Fatalf("expected over-cap rejection")
	}
	if got.Text != "keeper" {
		t.Fatalf("over-cap write must not clobber existing notes; got %q", got.Text)
	}
	if LoadOperatingNotes(db, ns).Text != "keeper" {
		t.Fatalf("store mutated by an over-cap write")
	}
}

func TestOperatingNotesEmptyClears(t *testing.T) {
	db := notesTestDB()
	ns := "agent:x"
	SaveOperatingNotes(db, ns, "something")
	SaveOperatingNotes(db, ns, "")
	if n := LoadOperatingNotes(db, ns); n.Text != "" {
		t.Fatalf("clear failed: %q", n.Text)
	}
}

func TestResolveOperatingNotesSeedIsEphemeral(t *testing.T) {
	db := notesTestDB()
	ns := "agent:x"

	// No stored notes → seed renders, but is NOT persisted.
	got := ResolveOperatingNotes(db, ns, "seed brief")
	if got.Text != "seed brief" {
		t.Fatalf("seed not returned: %q", got.Text)
	}
	if LoadOperatingNotes(db, ns).Text != "" {
		t.Fatalf("seed must not be persisted by resolve")
	}

	// Once stored, the stored value wins over the seed.
	SaveOperatingNotes(db, ns, "real note")
	if got := ResolveOperatingNotes(db, ns, "seed brief"); got.Text != "real note" {
		t.Fatalf("stored notes should win over seed; got %q", got.Text)
	}
}

func TestRenderOperatingNotesBlock(t *testing.T) {
	if RenderOperatingNotesBlock(OperatingNotes{}) != "" {
		t.Fatalf("empty notes must render nothing")
	}
	block := RenderOperatingNotesBlock(OperatingNotes{Text: "mid-draft on section 3"})
	if !strings.Contains(block, "## Working notes") || !strings.Contains(block, "mid-draft on section 3") {
		t.Fatalf("block missing header/body: %q", block)
	}
	// Advisory framing is the self-corruption guardrail — must be present.
	if !strings.Contains(block, "never override") {
		t.Fatalf("block must carry advisory framing: %q", block)
	}
}
