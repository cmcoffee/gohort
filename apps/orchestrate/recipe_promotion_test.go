package orchestrate

// The third rung for the two recipe primitives. Both already reached other
// people by name and both already ran in the REQUESTER's namespace, so
// publishing one is a wider grant on the same record rather than a move.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// recipeTestApp is newTestOrchestrate plus the save hook the running app wires
// at web-init. Without it the share index is never written, so a test would
// exercise a lookup that production never performs.
func recipeTestApp(t *testing.T) {
	t.Helper()
	newTestOrchestrate(t)
	prev := PipelineSavedHook
	PipelineSavedHook = syncPipelineShareIndex
	t.Cleanup(func() { PipelineSavedHook = prev })
}

func publishTestPipeline(t *testing.T, owner, name string) PipelineDef {
	t.Helper()
	return SavePipelineDef(UserDB(orchestrateBaseDB, owner), PipelineDef{
		Owner: owner, Name: name, Stages: []PipelineStage{{Name: "one"}}})
}

// Publishing reaches somebody who was never named anywhere, through the same
// function every surface already calls.
func TestAPublishedPipelineReachesEverybody(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")

	if userCanRunSharedPipeline(def, "dana") {
		t.Fatal("an unshared pipeline already reaches an ordinary user")
	}
	if err := publishPipelineForDeployment("alice", "Nightly"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for _, sp := range sharedPipelinesFor("dana") {
		if sp.Def.ID == def.ID {
			return
		}
	}
	t.Error("a published pipeline does not reach an ordinary user")
}

// The named list goes when it is published: everybody already includes them,
// and leaving it would have a later un-publishing restore an ACL the owner had
// forgotten.
func TestPublishingAPipelineClearsItsRecipientList(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	def.AllowedUsers = []string{"bob"}
	SavePipelineDef(UserDB(orchestrateBaseDB, "alice"), def)

	if err := publishPipelineForDeployment("alice", def.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, ok := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)
	if !ok {
		t.Fatal("the pipeline went missing")
	}
	if !got.Published || len(got.AllowedUsers) != 0 {
		t.Errorf("published=%v recipients=%v", got.Published, got.AllowedUsers)
	}
}

// Taking it back is the owner's, and only the owner's.
func TestOnlyTheOwnerTakesAPipelineBack(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	publishPipelineForDeployment("alice", def.ID)

	if err := unpublishPipeline("bob", def.ID); err == nil {
		t.Error("somebody unpublished a pipeline that was not theirs")
	}
	if err := unpublishPipeline("alice", def.ID); err != nil {
		t.Fatalf("take back: %v", err)
	}
	got, _ := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)
	if got.Published {
		t.Error("it is still published")
	}
	if userCanRunSharedPipeline(got, "dana") {
		t.Error("it still reaches everybody")
	}
}

// A copy of a published recipe is not itself published. An administrator agreed
// that THAT record should reach everybody, not every copy anyone makes of it.
func TestACopyOfAPublishedRecipeIsNotPublished(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	publishPipelineForDeployment("alice", def.ID)
	published, _ := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)

	dup := published
	dup.ID, dup.Owner, dup.AllowedUsers, dup.Published = "", "bob", nil, false
	saved := SavePipelineDef(UserDB(orchestrateBaseDB, "bob"), dup)
	if saved.Published {
		t.Error("the copy carried the approval with it")
	}
}

// An admin revoking a share has to reach the widest grant too, or it revokes
// nothing: the named list cleared and deployment-wide left standing.
func TestAdminRevokeAlsoUnpublishes(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	publishPipelineForDeployment("alice", def.ID)

	if err := revokePipelineShareForAdmin(orchestrateBaseDB, "alice", def.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)
	if got.Published {
		t.Error("an admin revoke left the pipeline deployment-wide")
	}
}

// Filed under the NAME, because an admin reading the queue needs to know what
// they are approving. A rename between filing and approval fails loudly.
func TestAPublishRequestResolvesByName(t *testing.T) {
	recipeTestApp(t)
	publishTestPipeline(t, "alice", "Nightly")

	if err := publishPipelineForDeployment("alice", "nightly"); err != nil {
		t.Errorf("a name should resolve, case and all: %v", err)
	}
	err := publishPipelineForDeployment("alice", "Renamed Since")
	if err == nil {
		t.Fatal("a stale name published something")
	}
	if !strings.Contains(err.Error(), "renamed") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}
