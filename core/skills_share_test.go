package core

// Peer sharing on skills, from the recipient's side. A recipient gets the
// BEHAVIOUR: the skill activates on their turns, and they cannot edit, delete
// or inherit the owner's documents with it.

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/snugforge/kvlite"
)

func skillShareStore(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	return db
}

func TestASharedSkillActivatesForItsRecipient(t *testing.T) {
	db := skillShareStore(t)
	if _, err := SaveSkill(db, "alice", SkillRecord{
		ID: "s1", Name: "Triage", Description: "Assess first", Instructions: "Assess.",
		AttachedCollections: []string{"col-1"}, AllowedUsers: []string{"bob"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := SharedSkillsFor(db, "bob")
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("the recipient does not have it: %+v", got)
	}
	// The behaviour, not the corpus. Handing somebody a skill is not handing
	// them the owner's documents.
	if len(got[0].AttachedCollections) != 0 {
		t.Errorf("the owner's collections travelled with the share: %+v", got[0].AttachedCollections)
	}
	// It shows up where activation reads.
	avail := AvailableSkills(db, "bob")
	if len(avail) != 1 || avail[0].ID != "s1" {
		t.Errorf("it is not in the recipient's available set: %+v", avail)
	}
	// Somebody unnamed gets nothing.
	if got := AvailableSkills(db, "dana"); len(got) != 0 {
		t.Errorf("an unnamed user has it: %+v", got)
	}
}

// The record is the source of truth and the index is derived. A derived thing
// that can outvote its source is how a revoked share keeps working.
func TestRevokingASkillShareTakesEffect(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.", AllowedUsers: []string{"bob"}})
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	if got := SharedSkillsFor(db, "bob"); len(got) != 0 {
		t.Errorf("a revoked share survives: %+v", got)
	}
}

// A muted skill is muted for everybody. An owner who disabled it did not
// disable it only for themselves.
func TestADisabledSkillIsNotSharedEither(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AllowedUsers: []string{"bob"}, Disabled: true})
	if got := SharedSkillsFor(db, "bob"); len(got) != 0 {
		t.Errorf("a disabled skill still activates for a recipient: %+v", got)
	}
}

// Deleting takes the shares with it: an index entry outliving its record points
// at nothing, which reads to a recipient as access they lost.
func TestDeletingASkillDropsItsShares(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.", AllowedUsers: []string{"bob"}})
	if !DeleteSkill(db, "alice", "s1") {
		t.Fatal("delete")
	}
	if got := peershare.List(db, SharedSkillsTable, "bob"); len(got) != 0 {
		t.Errorf("the index still points at a deleted skill: %+v", got)
	}
	if got := AvailableSkills(db, "bob"); len(got) != 0 {
		t.Errorf("the recipient still has it: %+v", got)
	}
}

// A recipient's own skill wins a name collision: it is the one they wrote.
func TestYourOwnSkillComesFirst(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's.", AllowedUsers: []string{"bob"}})
	SaveSkill(db, "bob", SkillRecord{ID: "own", Name: "Triage", Instructions: "Bob's."})
	avail := AvailableSkills(db, "bob")
	if len(avail) != 2 || avail[0].ID != "own" {
		t.Errorf("the recipient's own skill is not first: %+v", avail)
	}
}

// A bundled tool does not travel either. Running it would run the owner's code
// in the recipient's session, under the recipient's credentials, with no
// approval anywhere — which is the one thing a tool share has to pass through.
func TestASharedSkillCarriesNoBundledTools(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools:               []TempTool{{Name: "ssh_run"}, {Name: "page_oncall"}},
		AttachedCollections: []string{"col-1"},
		AllowedUsers:        []string{"bob"}})

	got := SharedSkillsFor(db, "bob")
	if len(got) != 1 {
		t.Fatalf("the recipient does not have it: %+v", got)
	}
	if len(got[0].Tools) != 0 {
		t.Errorf("the owner's tools travelled with the share: %+v", got[0].Tools)
	}
	// The owner's own copy is untouched: the strip is on the way out, not a
	// rewrite of what they built.
	own := LoadSkills(db, "alice")
	if len(own) != 1 || len(own[0].Tools) != 2 || len(own[0].AttachedCollections) != 1 {
		t.Errorf("the owner's own skill was stripped: %+v", own)
	}
}

// The recipient's model is TOLD what did not arrive. A skill whose steps
// reference a tool that is not there, with nothing saying so, is how a run
// invents a substitute and reports success.
func TestTheRecipientIsToldWhatDidNotArrive(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools: []TempTool{{Name: "ssh_run"}}, AttachedCollections: []string{"col-1"},
		AllowedUsers: []string{"bob"}})

	block := skillInstructionsBlock(SharedSkillsFor(db, "bob")[0], nil)
	for _, want := range []string{"alice", "bundled tool", "attached collection"} {
		if !strings.Contains(block, want) {
			t.Errorf("the recipient is not told about %q:\n%s", want, block)
		}
	}
	// The owner's own turn says none of this.
	if b := skillInstructionsBlock(LoadSkills(db, "alice")[0], nil); strings.Contains(b, "did not come with it") {
		t.Errorf("the owner is told their own skill is incomplete:\n%s", b)
	}
}

// And the OWNER is told, because they are the only one who can fix it: share
// the tool as a tool, the collection as a collection, or reword the skill.
func TestTheOwnerIsToldTheirShareIsIncomplete(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools: []TempTool{{Name: "ssh_run"}}, AllowedUsers: []string{"bob"}})
	SharedSkillsFor(db, "bob")

	notes := notices.List(db, "alice")
	if len(notes) != 1 || !strings.Contains(notes[0].Title, "Triage") {
		t.Fatalf("the owner was not told: %+v", notes)
	}
	// Not the recipient's problem to read about somebody else's configuration.
	if got := notices.List(db, "bob"); len(got) != 0 {
		t.Errorf("the recipient was told about the owner's setup: %+v", got)
	}
	// Fifty activations a day is one row with a count, not a stream.
	SharedSkillsFor(db, "bob")
	SharedSkillsFor(db, "bob")
	if notes := notices.List(db, "alice"); len(notes) != 1 {
		t.Errorf("repeats did not fold: %+v", notes)
	}
}

// A share with nothing withheld says nothing to anybody.
func TestACompleteShareIsQuiet(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AllowedUsers: []string{"bob"}})
	if got := SharedSkillsFor(db, "bob"); len(got) != 1 {
		t.Fatalf("share: %+v", got)
	}
	if notes := notices.List(db, "alice"); len(notes) != 0 {
		t.Errorf("a complete share nagged its owner: %+v", notes)
	}
	if b := skillInstructionsBlock(SharedSkillsFor(db, "bob")[0], nil); strings.Contains(b, "did not come with it") {
		t.Errorf("a complete share claims something is missing:\n%s", b)
	}
}
