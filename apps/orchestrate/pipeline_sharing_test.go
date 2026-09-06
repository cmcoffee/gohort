package orchestrate

import (
	"context"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
	"strings"
	"testing"
)

// A pipeline somebody shared with you is a dispatch target like any other: the
// same policy reads it, the same warden judges it, and it runs in YOUR
// namespace. These pin that it is reachable, and that being shared buys it no
// exemption from anything.

// sharedDispatchTurn: a caller agent belonging to "u", plus a pipeline that
// "colleague" shared with them.
func sharedDispatchTurn(t *testing.T, shareWith string, name string) (*chatTurn, PipelineDef) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prevBase, prevSaved := orchestrateBaseDB, PipelineSavedHook
	orchestrateBaseDB = root
	PipelineSavedHook = syncPipelineShareIndex
	t.Cleanup(func() { orchestrateBaseDB, PipelineSavedHook = prevBase, prevSaved })

	def := SavePipelineDef(UserDB(root, "colleague"), PipelineDef{
		Name: name, Owner: "colleague", AllowedUsers: []string{shareWith},
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "do {input}"}},
	})
	udb := UserDB(root, "u")
	caller, err := saveAgent(udb, AgentRecord{
		Name: "Caller", Owner: "u", DispatchMode: dispatchAll, OrchestratorPrompt: "p",
	})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	return &chatTurn{user: "u", udb: udb, agent: caller, ctx: context.Background()}, def
}

func TestSharedPipelineIsDispatchable(t *testing.T) {
	turn, def := sharedDispatchTurn(t, "u", "Nightly Report")

	got, err := turn.dispatchablePipeline("Nightly Report")
	if err != nil {
		t.Fatalf("a pipeline shared with this user must be dispatchable: %v", err)
	}
	if got.ID != def.ID || got.Owner != "colleague" {
		t.Fatalf("resolved %q owned by %q, want the shared one", got.Name, got.Owner)
	}
	// It is advertised, or the model has no way to know it exists.
	names := turn.dispatchablePipelineNames(maxAdvertisedPipelines)
	if len(names) != 1 || names[0] != "Nightly Report" {
		t.Errorf("advertised %v, want the shared pipeline", names)
	}
	// And it is known to be somebody else's — the run label and the log say so.
	if !turn.foreignPipeline(got) {
		t.Error("a pipeline owned by another user must read as foreign")
	}
	if label := pipelineRunLabel(turn, got); !strings.Contains(label, "colleague") {
		t.Errorf("the activity label should name the sharer, got %q", label)
	}
}

// Shared is not a bypass: a pipeline nobody shared with this user stays
// unreachable, exactly as it was before dispatch was widened.
func TestUnsharedPipelineIsNotDispatchable(t *testing.T) {
	turn, _ := sharedDispatchTurn(t, "somebody-else", "Nightly Report")
	if _, err := turn.dispatchablePipeline("Nightly Report"); err == nil {
		t.Fatal("an agent reached a pipeline its user was never given")
	}
	if names := turn.dispatchablePipelineNames(5); len(names) != 0 {
		t.Errorf("advertised a pipeline nobody shared: %v", names)
	}
}

// The dispatch policy reads a shared pipeline exactly as it reads an owned one.
func TestPolicyGovernsSharedPipelines(t *testing.T) {
	turn, def := sharedDispatchTurn(t, "u", "Nightly Report")
	turn.agent.DispatchMode = dispatchOnly
	turn.agent.AllowedDispatchTargets = []string{"some-agent"}
	if _, err := turn.dispatchablePipeline("Nightly Report"); err == nil {
		t.Fatal("Only mode let through a shared pipeline nobody ticked")
	}
	turn.agent.AllowedDispatchTargets = []string{def.ID}
	if _, err := turn.dispatchablePipeline("Nightly Report"); err != nil {
		t.Fatalf("a ticked shared pipeline must be reachable: %v", err)
	}
	// An expressed denial still bites, and it is keyed by id so it works on a
	// record this user does not own.
	turn.agent.DispatchMode = dispatchAll
	turn.agent.AllowedDispatchTargets = nil
	turn.agent.DisabledPipelines = []string{def.ID}
	if _, err := turn.dispatchablePipeline("Nightly Report"); err == nil {
		t.Error("a denied shared pipeline was still reachable")
	}
}

// The model has names, not ids. A shared pipeline whose name collides with one
// of the user's own can never be reached by the only handle the model has, so
// it is dropped from the set rather than advertised as an unreachable duplicate.
func TestOwnNameShadowsASharedPipeline(t *testing.T) {
	turn, _ := sharedDispatchTurn(t, "u", "Nightly Report")
	mine := SavePipelineDef(turn.udb, PipelineDef{
		Name: "Nightly Report", Owner: "u",
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "x"}},
	})
	got, err := turn.dispatchablePipeline("Nightly Report")
	if err != nil {
		t.Fatalf("the user's own pipeline must still resolve: %v", err)
	}
	if got.ID != mine.ID {
		t.Fatalf("a shared pipeline shadowed the user's own %q", got.Name)
	}
	if names := turn.dispatchablePipelineNames(5); len(names) != 1 {
		t.Errorf("the colliding shared pipeline should not be advertised: %v", names)
	}
}

// A schedule can fire a pipeline somebody shared — and cannot outlive the share.

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

// Peer-sharing a pipeline. The recipe travels and the authority does not, so
// what these pin is mostly what a share does NOT hand over.

// shareWorld gives two users on one store, with the index hook wired the way
// the running app wires it.
func shareWorld(t *testing.T) (root Database, ownerDB, peerDB Database) {
	t.Helper()
	root = &DBase{Store: kvlite.MemStore()}
	prevBase, prevSaved, prevDeleted := orchestrateBaseDB, PipelineSavedHook, PipelineDeletedHook
	orchestrateBaseDB = root
	PipelineSavedHook = syncPipelineShareIndex
	PipelineDeletedHook = pipelineDeleted
	t.Cleanup(func() {
		orchestrateBaseDB, PipelineSavedHook, PipelineDeletedHook = prevBase, prevSaved, prevDeleted
	})
	return root, UserDB(root, "owner"), UserDB(root, "peer")
}

func sharedPipeline(t *testing.T, udb Database, name string, with ...string) PipelineDef {
	t.Helper()
	return SavePipelineDef(udb, PipelineDef{
		Name: name, Owner: "owner", AllowedUsers: with,
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "do {input}"}},
	})
}

// A pipeline is private until its owner says otherwise — the state every
// pipeline was in before this existed, and the one a mistake should land in.
func TestUnsharedPipelineIsInvisibleToOthers(t *testing.T) {
	_, ownerDB, peerDB := shareWorld(t)
	def := sharedPipeline(t, ownerDB, "Private")

	if got := sharedPipelinesFor("peer"); len(got) != 0 {
		t.Fatalf("an unshared pipeline reached another user: %+v", got)
	}
	if _, _, _, found := resolvePipelineFor("peer", peerDB, def.ID); found {
		t.Fatal("a peer resolved a pipeline nobody shared with them")
	}
}

func TestSharedPipelineResolvesForItsRecipient(t *testing.T) {
	_, ownerDB, peerDB := shareWorld(t)
	def := sharedPipeline(t, ownerDB, "Nightly", "peer")

	got, owner, mine, found := resolvePipelineFor("peer", peerDB, def.ID)
	if !found {
		t.Fatal("a recipient could not resolve the pipeline shared with them")
	}
	if got.Name != "Nightly" || owner != "owner" {
		t.Fatalf("resolved %q owned by %q, want Nightly/owner", got.Name, owner)
	}
	// mine is what every write gates on. A recipient reads and runs; the
	// definition stays the owner's.
	if mine {
		t.Error("a recipient must not be treated as the owner")
	}
	// Somebody NOT on the list is still outside.
	if _, _, _, found := resolvePipelineFor("stranger", UserDB(nil, ""), def.ID); found {
		t.Error("a share reached a user it does not name")
	}
	// And the owner's own resolution is unchanged — own-first, marked mine.
	if _, _, mine, found := resolvePipelineFor("owner", ownerDB, def.ID); !found || !mine {
		t.Error("the owner lost ownership of their own pipeline")
	}
}

// Un-sharing needs no separate call: the index follows the record, so clearing
// the list is the whole operation.
func TestUnsharingRemovesReach(t *testing.T) {
	_, ownerDB, peerDB := shareWorld(t)
	def := sharedPipeline(t, ownerDB, "Nightly", "peer")
	if len(sharedPipelinesFor("peer")) != 1 {
		t.Fatal("setup: the share did not register")
	}
	def.AllowedUsers = nil
	SavePipelineDef(ownerDB, def)
	if got := sharedPipelinesFor("peer"); len(got) != 0 {
		t.Fatalf("un-sharing left the pipeline reachable: %+v", got)
	}
	if _, _, _, found := resolvePipelineFor("peer", peerDB, def.ID); found {
		t.Error("a revoked recipient still resolved the pipeline")
	}
}

// A deleted pipeline is shared with nobody, and the index must not be left
// pointing at a record that is gone.
func TestDeletingClearsTheShare(t *testing.T) {
	root, ownerDB, _ := shareWorld(t)
	def := sharedPipeline(t, ownerDB, "Nightly", "peer")
	DeletePipelineDef(ownerDB, def.ID)
	if got := sharedPipelinesFor("peer"); len(got) != 0 {
		t.Fatalf("a deleted pipeline is still shared: %+v", got)
	}
	if keys := root.Keys(sharedPipelinesTable); len(keys) != 0 {
		t.Errorf("the share index kept a dead entry: %v", keys)
	}
}

// An own record shadows a foreign shared one with the same id, so nothing
// another user does can change what an id means in your own store.
func TestOwnPipelineShadowsAShared(t *testing.T) {
	_, ownerDB, peerDB := shareWorld(t)
	shared := sharedPipeline(t, ownerDB, "Nightly", "peer")
	mineToo := SavePipelineDef(peerDB, PipelineDef{
		ID: shared.ID, Name: "My Own", Owner: "peer",
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "x"}},
	})
	got, owner, mine, found := resolvePipelineFor("peer", peerDB, mineToo.ID)
	if !found || !mine || owner != "peer" || got.Name != "My Own" {
		t.Fatalf("own record must win: name=%q owner=%q mine=%v", got.Name, owner, mine)
	}
}

// The recipient list is identity, not content: it survives an edit and is
// changed only through its own door.
func TestShareListIsNotEditableContent(t *testing.T) {
	if got := normalizeShareList([]string{" peer ", "peer", "owner", "", "other"}, "owner"); len(got) != 2 || got[0] != "peer" || got[1] != "other" {
		t.Fatalf("normalize gave %v, want [peer other] — trimmed, deduped, owner dropped", got)
	}
	if got := normalizeShareList(nil, "owner"); len(got) != 0 {
		t.Errorf("an empty list must stay empty, got %v", got)
	}
}

// A recipient list names users of THIS deployment. In another one those names
// are somebody else, so a recipe must not carry them.
func TestExportDropsTheRecipientList(t *testing.T) {
	def := PipelineDef{
		Name: "Nightly", Owner: "owner", AllowedUsers: []string{"peer"},
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "x"}},
	}
	if got := ExportPipeline(def); len(got.AllowedUsers) != 0 || got.Owner != "" {
		t.Fatalf("export carried identity: owner=%q allowed=%v", got.Owner, got.AllowedUsers)
	}
}

// The admin audit walks records, not the index — so it can show an unshared
// pipeline, and can reveal an index that has drifted.
func TestAdminAuditSeesEveryOwnedPipeline(t *testing.T) {
	root, ownerDB, _ := shareWorld(t)
	sharedPipeline(t, ownerDB, "Shared One", "peer")
	sharedPipeline(t, ownerDB, "Private One")

	// The walk enumerates AUTH users, so the store needs them to exist — the
	// audit is over people, not over whatever happens to be in a table.
	root.Set(AuthTable, "user:owner", AuthUser{Username: "owner"})
	root.Set(AuthTable, "user:peer", AuthUser{Username: "peer"})

	rows := listUserOwnedPipelinesForAdmin(root)
	if len(rows) != 2 {
		t.Fatalf("audit should list both of the owner's pipelines, got %+v", rows)
	}
	var shared, private bool
	for _, r := range rows {
		switch r.Name {
		case "Shared One":
			shared = r.Shared && r.SharedWith == "peer"
		case "Private One":
			private = !r.Shared
		}
	}
	if !shared || !private {
		t.Errorf("audit must show both shared and unshared: %+v", rows)
	}
}
