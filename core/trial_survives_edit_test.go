package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// Confirmation must survive a Builder edit.
//
// Trial says nobody has vouched for a tool yet, and clearing it is a user
// action taken in Extensions › Tools — never something tool_def carries. So a
// Builder edit reconstructing the record silently UN-CONFIRMED a tool the user
// had confirmed, and the consequence was invisible: a trial tool is kept out
// of the inline catalog and reachable only via load_tool, so the agent stopped
// seeing it and reached for whatever it could see instead.
//
// Reported live as "the moltbook agent keeps using fetch_url over the moltbook
// tools" — a tool that had been confirmed, was edited, and quietly left the
// catalog. Nothing failed; it simply was not offered.
func TestConfirmationSurvivesABuilderEdit(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const user = "cmcoffee@gmail.com"

	// A confirmed tool: the user vouched for it, so Trial is false.
	confirmed := TempTool{Name: "moltbook", Description: "post to moltbook"}
	if err := AdminPersistTempTool(db, user, confirmed); err != nil {
		t.Fatal(err)
	}

	// Builder edits it — reconstructing the record from tool_def args, which
	// know nothing about confirmation.
	edited := TempTool{Name: "moltbook", Description: "post to moltbook (v2)", Trial: true}
	if err := AdminPersistTempTool(db, user, edited); err != nil {
		t.Fatal(err)
	}

	for _, got := range LoadPersistentTempTools(db, user) {
		if got.Tool.Name != "moltbook" {
			continue
		}
		if got.Tool.Trial {
			t.Fatal("an edit un-confirmed a confirmed tool; it would drop out of the inline catalog and the agent would stop seeing it")
		}
		// The edit's real content still lands.
		if got.Tool.Description != "post to moltbook (v2)" {
			t.Errorf("the edit did not apply: %q", got.Tool.Description)
		}
	}
}

// And in the other direction: an edit is not a vouching either. A tool nobody
// has confirmed stays unconfirmed after Builder touches it.
func TestAnEditDoesNotConfirmAnUnvouchedTool(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const user = "u"
	since := time.Now().Add(-48 * time.Hour).UTC()

	trial := TempTool{Name: "draft", Trial: true, TrialSince: since}
	if err := AdminPersistTempTool(db, user, trial); err != nil {
		t.Fatal(err)
	}
	// Builder edits it without setting Trial at all.
	if err := AdminPersistTempTool(db, user, TempTool{Name: "draft", Description: "edited"}); err != nil {
		t.Fatal(err)
	}
	for _, got := range LoadPersistentTempTools(db, user) {
		if got.Tool.Name != "draft" {
			continue
		}
		if !got.Tool.Trial {
			t.Fatal("an edit confirmed a tool nobody vouched for")
		}
		// The reaper's clock must not restart on every edit, or an unconfirmed
		// tool edited regularly would never age out.
		if !got.Tool.TrialSince.Equal(since) {
			t.Errorf("TrialSince moved from %v to %v — the reaper clock restarted", since, got.Tool.TrialSince)
		}
	}
}

// The flags that were already preserved must stay preserved — this change adds
// to that list rather than replacing it.
func TestTheOtherGovernanceFlagsStillSurvive(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const user = "u"

	if err := AdminPersistTempTool(db, user, TempTool{
		Name: "t", Disabled: true, BuilderOnly: true, BoundOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(db, user, TempTool{Name: "t", Description: "edited"}); err != nil {
		t.Fatal(err)
	}
	for _, got := range LoadPersistentTempTools(db, user) {
		if got.Tool.Name != "t" {
			continue
		}
		if !got.Tool.Disabled || !got.Tool.BuilderOnly || !got.Tool.BoundOnly {
			t.Fatalf("an edit cleared a governance flag: %+v", got.Tool)
		}
	}
}
