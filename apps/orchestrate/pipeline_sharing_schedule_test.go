package orchestrate

// A schedule can fire a pipeline somebody shared — and cannot outlive the share.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// scheduleWorld wires the share index and RootDB together, since a schedule
// lives in one and the pipeline it fires lives in the other.
func scheduleWorld(t *testing.T) Database {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prevBase, prevRoot, prevSaved, prevDeleted := orchestrateBaseDB, RootDB, PipelineSavedHook, PipelineDeletedHook
	orchestrateBaseDB, RootDB = root, root
	PipelineSavedHook = syncPipelineShareIndex
	PipelineDeletedHook = pipelineDeleted
	t.Cleanup(func() {
		orchestrateBaseDB, RootDB, PipelineSavedHook, PipelineDeletedHook = prevBase, prevRoot, prevSaved, prevDeleted
	})
	root.Set(AuthTable, "user:owner", AuthUser{Username: "owner"})
	root.Set(AuthTable, "user:peer", AuthUser{Username: "peer"})
	return root
}

func schedulePipeline(t *testing.T, root Database, with ...string) PipelineDef {
	t.Helper()
	return SavePipelineDef(UserDB(root, "owner"), PipelineDef{
		Name: "Nightly", Owner: "owner", AllowedUsers: with,
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "do {input}"}},
	})
}

// The point: a recipient's schedule can name a pipeline they do not own.
func TestScheduleResolvesASharedPipeline(t *testing.T) {
	root := scheduleWorld(t)
	def := schedulePipeline(t, root, "peer")

	got, ok := pipelineForUser("peer", def.ID)
	if !ok || got.Owner != "owner" {
		t.Fatalf("a recipient's schedule could not resolve the shared pipeline: ok=%v owner=%q", ok, got.Owner)
	}
	// And the health check agrees, so the schedule list and the fire path
	// cannot disagree about whether it is armed against something real.
	if !pipelineExists("peer", def.ID) {
		t.Error("the dependency check calls a reachable shared pipeline missing")
	}
	// Somebody with no share has neither.
	if _, ok := pipelineForUser("stranger", def.ID); ok {
		t.Error("a schedule resolved a pipeline nobody shared with its owner")
	}
}

// Deleted and un-shared are the same silence to a resolver and completely
// different problems to the reader.
func TestMissingPipelineReasonDistinguishesRevokedFromDeleted(t *testing.T) {
	root := scheduleWorld(t)
	def := schedulePipeline(t, root, "peer")

	// Still exists, no longer shared: say who to ask.
	def.AllowedUsers = nil
	SavePipelineDef(UserDB(root, "owner"), def)
	if why := pipelineMissingReason("peer", def.ID); !strings.Contains(why, "no longer shares") || !strings.Contains(why, "owner") {
		t.Errorf("a withdrawn share should name the person who withdrew it; got %q", why)
	}
	// Actually gone: say that instead.
	DeletePipelineDef(UserDB(root, "owner"), def.ID)
	if why := pipelineMissingReason("peer", def.ID); !strings.Contains(why, "deleted") {
		t.Errorf("a deleted pipeline should read as deleted; got %q", why)
	}
}

// A share taken back takes back everything it enabled, including a schedule
// armed against it — otherwise the recipient's schedule looks healthy and fires
// into a refusal at whatever hour it is set for.
func TestRevokingAShareBreaksTheRecipientSchedule(t *testing.T) {
	root := scheduleWorld(t)
	def := schedulePipeline(t, root, "peer")
	SaveStandingAgent(root, StandingAgent{
		Owner: "peer", Name: "nightly-run", PipelineID: def.ID, Mission: "go",
	})

	before := def.AllowedUsers
	def.AllowedUsers = nil
	saved := SavePipelineDef(UserDB(root, "owner"), def)
	breakSchedulesForLostRecipients(saved, before)

	sa, found := GetStandingAgent(root, "peer", "nightly-run")
	if !found {
		t.Fatal("the recipient's schedule should be KEPT, not deleted")
	}
	if !sa.Broken {
		t.Errorf("the recipient's schedule should be marked broken: %+v", sa)
	}
}

// Deleting a shared pipeline has to reach the schedules of people the deleter
// never sees.
func TestDeletingASharedPipelineBreaksRecipientSchedules(t *testing.T) {
	root := scheduleWorld(t)
	def := schedulePipeline(t, root, "peer")
	SaveStandingAgent(root, StandingAgent{
		Owner: "peer", Name: "nightly-run", PipelineID: def.ID, Mission: "go",
	})

	DeletePipelineDef(UserDB(root, "owner"), def.ID)

	sa, found := GetStandingAgent(root, "peer", "nightly-run")
	if !found {
		t.Fatal("the recipient's schedule should be kept and marked, not removed")
	}
	if !sa.Broken {
		t.Errorf("a recipient's schedule survived the deletion looking healthy: %+v", sa)
	}
}
