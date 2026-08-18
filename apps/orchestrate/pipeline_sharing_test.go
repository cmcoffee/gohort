package orchestrate

// Peer-sharing a pipeline. The recipe travels and the authority does not, so
// what these pin is mostly what a share does NOT hand over.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
