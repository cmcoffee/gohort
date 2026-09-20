package core

// Peer sharing on skills, from the recipient's side. A recipient gets the
// BEHAVIOUR: the skill activates on their turns, and they cannot edit, delete
// or inherit the owner's documents with it.

import (
	"testing"

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
	if got := ListPeerShares(db, SharedSkillsTable, "bob"); len(got) != 0 {
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
