package servitor

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// An investigation could write a guide and a guide could be read, and nothing
// recorded that THIS session wrote THAT one. So "open what I just wrote" meant
// going to find it by name, and "where did this section come from" had nothing
// to point at.

func guideDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

// TestTheFirstPushCreatesTheLink — auto, on the first push, with no separate
// "start a guide from this session" step to remember.
func TestTheFirstPushCreatesTheLink(t *testing.T) {
	db := guideDB(t)
	linkSessionGuide(db, "sess-1", "doc-1", "DB Runbook", true)

	got := SessionGuides(db, "sess-1")
	if len(got) != 1 {
		t.Fatalf("got %d links, want 1", len(got))
	}
	if got[0].DocID != "doc-1" || got[0].Title != "DB Runbook" {
		t.Errorf("link = %+v", got[0])
	}
	if !got[0].Created {
		t.Error("a guide this session brought into existence is not marked Created — deleting " +
			"it discards work nobody else has, which is a different act from unlinking an append")
	}
}

// TestOneSessionMayFeedSeveralGuides — an outage writes the symptom in one and
// the fix in another, and forcing the choice at the first push would be choosing
// before the work is done.
func TestOneSessionMayFeedSeveralGuides(t *testing.T) {
	db := guideDB(t)
	linkSessionGuide(db, "sess-1", "doc-1", "Symptoms", true)
	linkSessionGuide(db, "sess-1", "doc-2", "Fixes", false)

	got := SessionGuides(db, "sess-1")
	if len(got) != 2 {
		t.Fatalf("got %d links, want both guides", len(got))
	}
	// Oldest first — the order they were written is the order they were
	// thought of.
	if got[0].DocID != "doc-1" || got[1].DocID != "doc-2" {
		t.Errorf("links are not in write order: %+v", got)
	}
}

// TestRepeatedPushesAreOneLink — four sections into one guide is one
// relationship, not four rows.
func TestRepeatedPushesAreOneLink(t *testing.T) {
	db := guideDB(t)
	for i := 0; i < 4; i++ {
		linkSessionGuide(db, "sess-1", "doc-1", "Runbook", i == 0)
	}
	if got := SessionGuides(db, "sess-1"); len(got) != 1 {
		t.Errorf("got %d links for four pushes into one guide", len(got))
	}
}

// TestARenameIsPickedUpButCreatedIsNot — the id is the identity and the title is
// a label, so a guide renamed after the fact still reads correctly. Created is
// history and a later append must not rewrite it.
func TestARenameIsPickedUpButCreatedIsNot(t *testing.T) {
	db := guideDB(t)
	linkSessionGuide(db, "sess-1", "doc-1", "Runbook", true)
	linkSessionGuide(db, "sess-1", "doc-1", "Ops Runbook", false)

	got := SessionGuides(db, "sess-1")
	if len(got) != 1 {
		t.Fatalf("got %d links", len(got))
	}
	if got[0].Title != "Ops Runbook" {
		t.Errorf("the rename was not picked up: %q", got[0].Title)
	}
	if !got[0].Created {
		t.Error("a later append rewrote the fact that this session created the guide")
	}
}

// TestSessionsDoNotShareLinks — the link is per investigation.
func TestSessionsDoNotShareLinks(t *testing.T) {
	db := guideDB(t)
	linkSessionGuide(db, "sess-1", "doc-1", "A", true)
	if got := SessionGuides(db, "sess-2"); len(got) != 0 {
		t.Errorf("another session sees %d links", len(got))
	}
}

// TestForgettingASessionDropsItsLinks — and only a session's. A guide is the
// durable artifact and outlives the investigation that produced it.
func TestForgettingASessionDropsItsLinks(t *testing.T) {
	db := guideDB(t)
	linkSessionGuide(db, "sess-1", "doc-1", "A", true)
	linkSessionGuide(db, "sess-2", "doc-1", "A", false)

	forgetSessionGuides(db, "sess-1")
	if got := SessionGuides(db, "sess-1"); len(got) != 0 {
		t.Errorf("the discarded session still has %d links", len(got))
	}
	if got := SessionGuides(db, "sess-2"); len(got) != 1 {
		t.Error("forgetting one session dropped another's link to the same guide")
	}
}

// TestBadInputIsIgnored — a missing id must not write a row keyed on nothing.
func TestBadInputIsIgnored(t *testing.T) {
	db := guideDB(t)
	linkSessionGuide(db, "", "doc-1", "A", true)
	linkSessionGuide(db, "sess-1", "", "A", true)
	linkSessionGuide(nil, "sess-1", "doc-1", "A", true)
	if got := SessionGuides(db, "sess-1"); len(got) != 0 {
		t.Errorf("a malformed link was stored: %+v", got)
	}
	if got := SessionGuides(nil, "sess-1"); got != nil {
		t.Error("reading from a nil store returned rows")
	}
}
