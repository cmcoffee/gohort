package core

// Revision capture and rollback for the three definitions core owns: skills,
// machines and pipelines. The fourth (agents) lives in the orchestrate app and
// is tested beside it.

import (
	"testing"

	"github.com/cmcoffee/gohort/core/revisions"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestMachineDefKeepsAndRestoresVersions(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}

	made := SaveMachineDef(udb, MachineDef{Name: "Triage", Start: "decompose"})
	if revs := revisions.List(udb, revisions.KindMachine, made.ID); len(revs) != 0 {
		t.Fatalf("a new machine starts with history: %+v", revs)
	}

	edited := made
	edited.Name = "Triage v2"
	SaveMachineDefAs(udb, edited, "renamed")
	revs := revisions.List(udb, revisions.KindMachine, made.ID)
	if len(revs) != 1 || revs[0].Reason != "renamed" {
		t.Fatalf("revisions = %+v", revs)
	}

	back, err := RollbackMachineDef(udb, made.ID, "")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if back.Name != "Triage" {
		t.Errorf("restored name = %q, want Triage", back.Name)
	}
	// The restore files what it replaced, so going back is reversible.
	if after := revisions.List(udb, revisions.KindMachine, made.ID); len(after) != 2 {
		t.Errorf("got %d revisions after rollback, want 2", len(after))
	}
}

func TestMachineDefResaveAndDeleteHandleHistory(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	made := SaveMachineDef(udb, MachineDef{Name: "Triage", Start: "decompose"})

	// A save that changed nothing must not evict a version worth keeping.
	SaveMachineDef(udb, made)
	if revs := revisions.List(udb, revisions.KindMachine, made.ID); len(revs) != 0 {
		t.Errorf("an unchanged re-save filed %d revision(s)", len(revs))
	}

	edited := made
	edited.Name = "Triage v2"
	SaveMachineDef(udb, edited)
	DeleteMachineDef(udb, made.ID)
	if revs := revisions.List(udb, revisions.KindMachine, made.ID); len(revs) != 0 {
		t.Errorf("history outlived the machine: %+v", revs)
	}
}

func TestPipelineDefKeepsAndRestoresVersions(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}

	made := SavePipelineDef(udb, PipelineDef{Name: "Digest"})
	edited := made
	edited.Name = "Digest v2"
	SavePipelineDefAs(udb, edited, "renamed")

	revs := revisions.List(udb, revisions.KindPipeline, made.ID)
	if len(revs) != 1 || revs[0].Reason != "renamed" {
		t.Fatalf("revisions = %+v", revs)
	}
	back, err := RollbackPipelineDef(udb, made.ID, "")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if back.Name != "Digest" {
		t.Errorf("restored name = %q, want Digest", back.Name)
	}
	DeletePipelineDef(udb, made.ID)
	if revs := revisions.List(udb, revisions.KindPipeline, made.ID); len(revs) != 0 {
		t.Errorf("history outlived the pipeline: %+v", revs)
	}
}

func TestSkillKeepsAndRestoresVersions(t *testing.T) {
	db := skillTestDB(t)

	made, err := SaveSkill(db, "alice", SkillRecord{Name: "pdf", Instructions: "extract text first"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := made
	edited.Instructions = "summarize first"
	if _, err := SaveSkillAs(db, "alice", edited, "edited instructions"); err != nil {
		t.Fatalf("edit: %v", err)
	}

	key := skillRingKey("alice", made.ID)
	revs := revisions.List(db, revisions.KindSkill, key)
	if len(revs) != 1 || revs[0].Reason != "edited instructions" {
		t.Fatalf("revisions = %+v", revs)
	}

	back, err := RollbackSkill(db, "alice", made.ID, "")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if back.Instructions != "extract text first" {
		t.Errorf("restored instructions = %q", back.Instructions)
	}
	if DeleteSkill(db, "alice", made.ID); len(revisions.List(db, revisions.KindSkill, key)) != 0 {
		t.Error("history outlived the skill")
	}
}

// Skills are the odd one out: they live in RootDB keyed by username, not in a
// per-user store. If the owner were not in the ring key, two users editing
// skills that share an id would share a history — and one could restore the
// other's definition.
func TestSkillHistoryIsPerOwner(t *testing.T) {
	db := skillTestDB(t)

	for _, who := range []string{"alice", "bob"} {
		made, err := SaveSkill(db, who, SkillRecord{ID: "skill-shared", Name: who, Instructions: who + " v1"})
		if err != nil {
			t.Fatalf("%s create: %v", who, err)
		}
		edited := made
		edited.Instructions = who + " v2"
		if _, err := SaveSkill(db, who, edited); err != nil {
			t.Fatalf("%s edit: %v", who, err)
		}
	}

	for _, who := range []string{"alice", "bob"} {
		revs := revisions.List(db, revisions.KindSkill, skillRingKey(who, "skill-shared"))
		if len(revs) != 1 {
			t.Fatalf("%s has %d revisions, want 1 — the rings are shared", who, len(revs))
		}
		var kept SkillRecord
		if !revisions.Load(db, revisions.KindSkill, skillRingKey(who, "skill-shared"), "", &kept) {
			t.Fatalf("%s: kept version did not load", who)
		}
		if kept.Instructions != who+" v1" {
			t.Errorf("%s sees %q — that is somebody else's definition", who, kept.Instructions)
		}
	}

	// And a rollback stays inside its owner's history.
	if _, err := RollbackSkill(db, "alice", "skill-shared", ""); err != nil {
		t.Fatalf("alice rollback: %v", err)
	}
	for _, s := range LoadSkills(db, "bob") {
		if s.ID == "skill-shared" && s.Instructions != "bob v2" {
			t.Errorf("alice's rollback changed bob's skill: %q", s.Instructions)
		}
	}
}

// Previous is the one-deep undo snapshot the describe-a-change door stashes on
// the record. Keeping it would store a second whole machine inside every ring
// entry, and stashing it would read as an edit on a save that changed nothing
// else.
func TestMachineUndoSnapshotStaysOutOfTheRing(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	made := SaveMachineDef(udb, MachineDef{Name: "Triage", Start: "decompose"})

	// A save whose only difference is the stashed snapshot is not an edit.
	stashed := made
	prior := made
	stashed.Previous = &prior
	SaveMachineDef(udb, stashed)
	if revs := revisions.List(udb, revisions.KindMachine, made.ID); len(revs) != 0 {
		t.Fatalf("stashing the undo snapshot filed %d revision(s)", len(revs))
	}

	// A real edit files one, and what it files carries no nested copy.
	edited := stashed
	edited.Name = "Triage v2"
	SaveMachineDef(udb, edited)
	var kept MachineDef
	if !revisions.Load(udb, revisions.KindMachine, made.ID, "", &kept) {
		t.Fatal("kept version did not load")
	}
	if kept.Previous != nil {
		t.Error("the ring entry carries a second whole machine inside it")
	}
	if kept.Name != "Triage" {
		t.Errorf("kept name = %q", kept.Name)
	}
}
