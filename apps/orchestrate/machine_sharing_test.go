package orchestrate

// Peer-sharing a machine. The recipe travels and the authority does not, so
// what these pin is mostly what a share does NOT hand over — the same shape
// pipeline_sharing_test.go pins for the other kind, deliberately, because the
// two rules are one rule and a drift between them is the bug.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// machineShareWorld gives two users on one store, with the index hooks wired
// the way the running app wires them.
func machineShareWorld(t *testing.T) (root Database, ownerDB, peerDB Database) {
	t.Helper()
	root = &DBase{Store: kvlite.MemStore()}
	prevBase, prevSaved, prevDeleted := orchestrateBaseDB, MachineSavedHook, MachineDeletedHook
	prevRoot := RootDB
	orchestrateBaseDB = root
	RootDB = root
	MachineSavedHook = syncMachineShareIndex
	MachineDeletedHook = machineDeleted
	t.Cleanup(func() {
		orchestrateBaseDB, MachineSavedHook, MachineDeletedHook = prevBase, prevSaved, prevDeleted
		RootDB = prevRoot
	})
	return root, UserDB(root, "owner"), UserDB(root, "peer")
}

// sharedMachine saves a runnable unattended machine owned by "owner".
func sharedMachine(t *testing.T, udb Database, name string, with ...string) MachineDef {
	t.Helper()
	return SaveMachineDef(udb, MachineDef{
		Owner: "owner", Name: name, AllowedUsers: with, Unattended: true,
		Phases: []MachinePhase{{Name: "work", Prompt: "do {input}"}},
	})
}

// A machine is private until its owner says otherwise — the state every
// machine was in before this existed, and the one a mistake should land in.
func TestUnsharedMachineIsInvisibleToOthers(t *testing.T) {
	_, ownerDB, peerDB := machineShareWorld(t)
	def := sharedMachine(t, ownerDB, "Private")

	if got := sharedMachinesFor("peer"); len(got) != 0 {
		t.Fatalf("an unshared machine reached another user: %+v", got)
	}
	if _, _, _, found := resolveMachineFor("peer", peerDB, def.ID); found {
		t.Fatal("a peer resolved a machine nobody shared with them")
	}
}

func TestSharedMachineResolvesForItsRecipient(t *testing.T) {
	_, ownerDB, peerDB := machineShareWorld(t)
	def := sharedMachine(t, ownerDB, "Outage Review", "peer")

	got, owner, mine, found := resolveMachineFor("peer", peerDB, def.ID)
	if !found {
		t.Fatal("a recipient could not resolve the machine shared with them")
	}
	if got.Name != "Outage Review" || owner != "owner" {
		t.Fatalf("resolved %q owned by %q, want Outage Review/owner", got.Name, owner)
	}
	// mine is what every write gates on. A recipient reads and runs; the
	// definition stays the owner's.
	if mine {
		t.Error("a recipient must not be treated as the owner")
	}
	if _, _, _, found := resolveMachineFor("stranger", UserDB(nil, ""), def.ID); found {
		t.Error("a share reached a user it does not name")
	}
	if _, _, mine, found := resolveMachineFor("owner", ownerDB, def.ID); !found || !mine {
		t.Error("the owner lost ownership of their own machine")
	}
}

// Un-sharing needs no separate call: the index follows the record.
func TestUnsharingAMachineRemovesReach(t *testing.T) {
	_, ownerDB, peerDB := machineShareWorld(t)
	def := sharedMachine(t, ownerDB, "Outage Review", "peer")
	if len(sharedMachinesFor("peer")) != 1 {
		t.Fatal("setup: the share did not register")
	}
	def.AllowedUsers = nil
	SaveMachineDef(ownerDB, def)
	if got := sharedMachinesFor("peer"); len(got) != 0 {
		t.Fatalf("un-sharing left the machine reachable: %+v", got)
	}
	if _, _, _, found := resolveMachineFor("peer", peerDB, def.ID); found {
		t.Error("a revoked recipient still resolved the machine")
	}
}

// A deleted machine is shared with nobody, and the index must not be left
// pointing at a record that is gone.
func TestDeletingAMachineClearsTheShare(t *testing.T) {
	root, ownerDB, _ := machineShareWorld(t)
	def := sharedMachine(t, ownerDB, "Outage Review", "peer")
	DeleteMachineDef(ownerDB, def.ID)
	if got := sharedMachinesFor("peer"); len(got) != 0 {
		t.Fatalf("a deleted machine is still shared: %+v", got)
	}
	if keys := root.Keys(sharedMachinesTable); len(keys) != 0 {
		t.Errorf("the share index kept a dead entry: %v", keys)
	}
}

// An own record shadows a foreign shared one with the same id.
func TestOwnMachineShadowsAShared(t *testing.T) {
	_, ownerDB, peerDB := machineShareWorld(t)
	shared := sharedMachine(t, ownerDB, "Outage Review", "peer")
	mineToo := SaveMachineDef(peerDB, MachineDef{
		ID: shared.ID, Name: "My Own", Owner: "peer", Unattended: true,
		Phases: []MachinePhase{{Name: "work", Prompt: "x"}},
	})
	got, owner, mine, found := resolveMachineFor("peer", peerDB, mineToo.ID)
	if !found || !mine || owner != "peer" || got.Name != "My Own" {
		t.Fatalf("own record must win: name=%q owner=%q mine=%v", got.Name, owner, mine)
	}
}

// A recipient list names users of THIS deployment. In another one those names
// are somebody else, so a recipe must not carry them — and an import must not
// accept them either, since a hand-written file is not bound by export.
func TestMachineExportAndImportDropTheRecipientList(t *testing.T) {
	def := MachineDef{
		Name: "Outage Review", Owner: "owner", AllowedUsers: []string{"peer"},
		Phases: []MachinePhase{{Name: "work", Prompt: "x", Resident: true}},
	}
	if got := ExportMachine(def); len(got.AllowedUsers) != 0 || got.Owner != "" {
		t.Fatalf("export carried identity: owner=%q allowed=%v", got.Owner, got.AllowedUsers)
	}
	_, ownerDB, _ := machineShareWorld(t)
	saved, err := ImportMachine(ownerDB, "owner", def)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(saved.AllowedUsers) != 0 {
		t.Errorf("import accepted a recipient list off a file: %v", saved.AllowedUsers)
	}
}

// A schedule must not outlive its permission: revoking a share marks the
// recipient's schedule broken rather than leaving it to fail at 3am.
func TestRevokingAMachineShareBreaksTheRecipientSchedule(t *testing.T) {
	root, ownerDB, _ := machineShareWorld(t)
	def := sharedMachine(t, ownerDB, "Outage Review", "peer")
	SaveStandingAgent(root, StandingAgent{
		Owner: "peer", Name: "nightly review", MachineID: def.ID, Mission: "go",
	})
	root.Set(AuthTable, "user:owner", AuthUser{Username: "owner"})
	root.Set(AuthTable, "user:peer", AuthUser{Username: "peer"})

	if err := revokeMachineShareForAdmin(root, "owner", def.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	sa, ok := GetStandingAgent(root, "peer", "nightly review")
	if !ok {
		t.Fatal("the schedule was deleted; broken-and-kept is the posture")
	}
	if !sa.Broken {
		t.Error("a schedule that lost its machine still reads as healthy")
	}
	// And the health check agrees, from the resolver the fire path uses.
	if reason := standingAgentDependencyError(sa); reason == "" {
		t.Error("the dependency check did not notice the withdrawn share")
	}
}

// Deleting a shared machine reaches EVERY user's schedules, not just the
// owner's — the case that is easiest to miss precisely because the person who
// broke it never sees the thing they broke.
func TestDeletingAMachineBreaksEverybodysSchedules(t *testing.T) {
	root, ownerDB, _ := machineShareWorld(t)
	def := sharedMachine(t, ownerDB, "Outage Review", "peer")
	root.Set(AuthTable, "user:owner", AuthUser{Username: "owner"})
	root.Set(AuthTable, "user:peer", AuthUser{Username: "peer"})
	// Armed by NAME, which is what a schedule holds when somebody typed it.
	SaveStandingAgent(root, StandingAgent{
		Owner: "peer", Name: "nightly review", MachineID: def.Name, Mission: "go",
	})

	DeleteMachineDef(ownerDB, def.ID)

	sa, ok := GetStandingAgent(root, "peer", "nightly review")
	if !ok {
		t.Fatal("the recipient's schedule was deleted rather than marked")
	}
	if !sa.Broken {
		t.Error("a name-armed schedule survived the deletion of its machine looking healthy")
	}
}

// The admin audit walks records, not the index — so it can show an unshared
// machine, and can reveal an index that has drifted.
func TestAdminAuditSeesEveryOwnedMachine(t *testing.T) {
	root, ownerDB, _ := machineShareWorld(t)
	sharedMachine(t, ownerDB, "Shared One", "peer")
	sharedMachine(t, ownerDB, "Private One")
	root.Set(AuthTable, "user:owner", AuthUser{Username: "owner"})
	root.Set(AuthTable, "user:peer", AuthUser{Username: "peer"})

	rows := listUserOwnedMachinesForAdmin(root)
	if len(rows) != 2 {
		t.Fatalf("audit should list both of the owner's machines, got %+v", rows)
	}
	var shared, private bool
	for _, r := range rows {
		switch r.Name {
		case "Shared One":
			shared = r.Shared && r.SharedWith == "peer" && r.Unattended
		case "Private One":
			private = !r.Shared
		}
	}
	if !shared || !private {
		t.Errorf("audit must show both shared and unshared: %+v", rows)
	}
}
