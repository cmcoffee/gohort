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

	got := sharedSkillsFor(db, "bob")
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("the recipient does not have it: %+v", got)
	}
	// The collection IDS travel; whether any of them resolves is decided in the
	// recipient's own namespace at retrieval time. Stripping them here meant a
	// skill and a collection deliberately shared with the same person still
	// could not work together.
	if len(got[0].AttachedCollections) != 1 {
		t.Errorf("the collection ids did not travel: %+v", got[0].AttachedCollections)
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
	if got := sharedSkillsFor(db, "bob"); len(got) != 0 {
		t.Errorf("a revoked share survives: %+v", got)
	}
}

// A muted skill is muted for everybody. An owner who disabled it did not
// disable it only for themselves.
func TestADisabledSkillIsNotSharedEither(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AllowedUsers: []string{"bob"}, Disabled: true})
	if got := sharedSkillsFor(db, "bob"); len(got) != 0 {
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
	if got := peershare.List(db, sharedSkillsTable, "bob"); len(got) != 0 {
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

	got := sharedSkillsFor(db, "bob")
	if len(got) != 1 {
		t.Fatalf("the recipient does not have it: %+v", got)
	}
	if len(got[0].Tools) != 0 {
		t.Errorf("the owner's tools travelled with the share: %+v", got[0].Tools)
	}
	// The collections are not the tools: an id resolves or it does not, and the
	// read path decides that per user. Only executable code is withheld here.
	if len(got[0].AttachedCollections) != 1 {
		t.Errorf("the collection ids were stripped as well: %+v", got[0].AttachedCollections)
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

	block := skillInstructionsBlock(sharedSkillsFor(db, "bob")[0], nil)
	for _, want := range []string{"alice", "bundled tool"} {
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
	sharedSkillsFor(db, "bob")

	notes := notices.List(db, "alice")
	if len(notes) != 1 || !strings.Contains(notes[0].Title, "Triage") {
		t.Fatalf("the owner was not told: %+v", notes)
	}
	// Not the recipient's problem to read about somebody else's configuration.
	if got := notices.List(db, "bob"); len(got) != 0 {
		t.Errorf("the recipient was told about the owner's setup: %+v", got)
	}
	// Fifty activations a day is one row with a count, not a stream.
	sharedSkillsFor(db, "bob")
	sharedSkillsFor(db, "bob")
	if notes := notices.List(db, "alice"); len(notes) != 1 {
		t.Errorf("repeats did not fold: %+v", notes)
	}
}

// A share with nothing withheld says nothing to anybody.
func TestACompleteShareIsQuiet(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AllowedUsers: []string{"bob"}})
	if got := sharedSkillsFor(db, "bob"); len(got) != 1 {
		t.Fatalf("share: %+v", got)
	}
	if notes := notices.List(db, "alice"); len(notes) != 0 {
		t.Errorf("a complete share nagged its owner: %+v", notes)
	}
	if b := skillInstructionsBlock(sharedSkillsFor(db, "bob")[0], nil); strings.Contains(b, "did not come with it") {
		t.Errorf("a complete share claims something is missing:\n%s", b)
	}
}

// ----------------------------------------------------------------------
// The deployment rung
// ----------------------------------------------------------------------

// Publishing MOVES the record. One copy means an edit later is an edit
// everybody gets, and one answer to "whose is this".
func TestPublishingASkillMovesItToTheDeployment(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})

	if err := promoteSkillToDeployment("alice", "Triage"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := LoadSkills(db, "alice"); len(got) != 0 {
		t.Errorf("a copy stayed in the author's pool: %+v", got)
	}
	pub := DeploymentSkills(db)
	if len(pub) != 1 || pub[0].Name != "Triage" {
		t.Fatalf("the deployment does not have it: %+v", pub)
	}
	// The author's name stays on it. A published skill nobody is answerable
	// for is worse than none.
	if pub[0].Owner != "alice" {
		t.Errorf("authorship was dropped: %q", pub[0].Owner)
	}
	// And it reaches somebody who was never named anywhere.
	avail := AvailableSkills(db, "dana")
	if len(avail) != 1 || avail[0].Name != "Triage" {
		t.Errorf("a published skill does not reach an ordinary user: %+v", avail)
	}
}

// The bundled tools do not go. Everything true of that for one recipient is
// more true for every account at once: it would run the author's scripts in
// every session in the deployment, under each person's own credentials.
func TestAPublishedSkillCarriesNoTools(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools:               []TempTool{{Name: "ssh_run"}},
		AttachedCollections: []string{"col-1"},
		AllowedUsers:        []string{"bob"}})
	promoteSkillToDeployment("alice", "s1")

	pub := DeploymentSkills(db)
	if len(pub) != 1 {
		t.Fatalf("publish: %+v", pub)
	}
	if len(pub[0].Tools) != 0 {
		t.Errorf("the author's tools went deployment-wide: %+v", pub[0].Tools)
	}
	// The collection ids stay: they are references, gated where they are read.
	if len(pub[0].AttachedCollections) != 1 {
		t.Errorf("the collection references were dropped: %+v", pub[0].AttachedCollections)
	}
	// The peer list goes, because everybody has it now — and leaving it would
	// have a later un-publishing silently restore an ACL the owner had forgotten.
	if len(pub[0].AllowedUsers) != 0 {
		t.Errorf("the old recipient list survived publication: %+v", pub[0].AllowedUsers)
	}
	if got := peershare.List(db, sharedSkillsTable, "bob"); len(got) != 0 {
		t.Errorf("the share index still points at the moved skill: %+v", got)
	}
}

// Your own skill wins over one the deployment publishes. Three tiers, most
// specific first.
func TestYourOwnSkillBeatsTheDeploymentsCopy(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's."})
	promoteSkillToDeployment("alice", "s1")
	SaveSkill(db, "bob", SkillRecord{ID: "own", Name: "Triage", Instructions: "Bob's."})

	avail := AvailableSkills(db, "bob")
	if len(avail) != 2 || avail[0].ID != "own" {
		t.Fatalf("the user's own skill is not first: %+v", avail)
	}
}

// A name the deployment already publishes is refused. A turn matching that name
// has to have one answer.
func TestPublishingRefusesADuplicateName(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's."})
	promoteSkillToDeployment("alice", "s1")
	SaveSkill(db, "carol", SkillRecord{ID: "s2", Name: "triage", Instructions: "Carol's."})

	err := promoteSkillToDeployment("carol", "s2")
	if err == nil {
		t.Fatal("a second skill of the same name was published")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	// And the refused one is untouched.
	if got := LoadSkills(db, "carol"); len(got) != 1 {
		t.Errorf("the refused publish consumed the author's skill anyway: %+v", got)
	}
}

// Taking it back is the author's alone: nobody needs permission to stop
// publishing something they wrote.
func TestAnAuthorCanTakeTheirSkillBack(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	promoteSkillToDeployment("alice", "s1")

	if err := NarrowSkillToOwner(db, "bob", "s1"); err == nil {
		t.Error("somebody unpublished a skill that was not theirs")
	}
	if err := NarrowSkillToOwner(db, "alice", "s1"); err != nil {
		t.Fatalf("take back: %v", err)
	}
	if got := DeploymentSkills(db); len(got) != 0 {
		t.Errorf("it is still published: %+v", got)
	}
	if got := LoadSkills(db, "alice"); len(got) != 1 || got[0].Name != "Triage" {
		t.Errorf("it did not come home: %+v", got)
	}
	if got := AvailableSkills(db, "dana"); len(got) != 0 {
		t.Errorf("it still reaches everybody: %+v", got)
	}
}

// The author's own mute still counts once it is published: an admin approved
// publishing a skill, not publishing it regardless of what its author later
// decided about it.
func TestADisabledPublishedSkillActivatesForNobody(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	promoteSkillToDeployment("alice", "s1")

	pub := DeploymentSkills(db)
	pub[0].Disabled = true
	skillStore(db).Set(deploymentSkillsTable, "all", pub)

	if got := AvailableSkills(db, "dana"); len(got) != 0 {
		t.Errorf("a muted deployment skill still activates: %+v", got)
	}
}
