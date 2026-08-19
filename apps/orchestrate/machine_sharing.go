// machine_sharing.go — peer-sharing of user-owned machines, the third and last
// kind in the user plane. agent_sharing.go and pipeline_sharing.go are the other
// two, and this file deliberately mirrors them rather than inventing a third
// vocabulary for the same idea.
//
// A machine's recipient set lives in MachineDef.AllowedUsers; empty means
// private to the owner, which is what every machine was before this. The owner
// edits it with the "Share with users" ACLPicker on the machine page.
//
// WHAT A SHARE GIVES. The recipe, not the authority — the same rule the other
// two state, and the reason it can be stated once and applied three times. A
// recipient reads the definition, runs it, and can copy it; the run happens in
// the RECIPIENT's namespace, so their catalog resolves its steps' tool names,
// their credentials back those tools, their agents answer a delegating step,
// and their warden judges what it does. Nothing of the owner's travels except
// the text of the machine.
//
// WHY A MACHINE IS WORTH SHARING AT ALL. It is the primitive that holds a
// POSITION: a pipeline is dataflow that forgets, an agent decides afresh every
// turn, and a machine carries a working set from step to step. "How we
// investigate an outage" is a procedure a team has, not a thing one person has,
// and until now the only way to give somebody yours was to export a file and
// have them import a copy that immediately began to drift from it.
//
// WHAT IT DOESN'T GIVE. Editing, attaching, deleting. A recipient cannot point
// their agent's brain at somebody else's machine either, and that one is not
// symmetry: AgentRecord.Machine is resolved in the AGENT owner's store on every
// turn, so an attachment across stores would be a pointer their session cannot
// follow. Duplicating it — which re-owns the copy — is the supported way to
// make somebody else's procedure your agent's.
//
// WHERE A SHARED MACHINE CAN BE SPENT. Everywhere an owned one can: the page's
// Run button, a schedule (standing_runner.go), and an agent's dispatch
// (agent_dispatch_machine.go). Pipelines reached that state in three steps, one
// version at a time, and every boundary drawn on the way turned out not to
// survive the question "which guard actually turns on whose recipe it is?".
// None does. So machines arrive at the end state directly.
//
// WHY AN INDEX. A recipient looking for "machines shared with me" is asking
// about records in other users' stores. Walking every store on every lookup is
// what the admin audit does — correct there, once, for a console — so the share
// is registered in one small table in the app's own store, kept current from
// MachineSavedHook (every save path funnels through SaveMachineDef) and
// MachineDeletedHook.
package orchestrate

import (
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	AdminListUserOwnedMachines = listUserOwnedMachinesForAdmin
	AdminRevokeMachineShare = revokeMachineShareForAdmin
}

// sharedMachinesTable maps a shared machine's id to its owner, in the app's own
// store rather than any user's. Presence means "shared with at least one
// person"; who it is shared WITH stays on the record, so there is one copy of
// the recipient list and the index can never contradict it.
const sharedMachinesTable = "shared_machines"

// syncMachineShareIndex is wired to MachineSavedHook: it adds the machine to
// the index when it names recipients and removes it when the last one is taken
// off, so un-sharing needs no separate call and cannot be forgotten.
func syncMachineShareIndex(def MachineDef) {
	if orchestrateBaseDB == nil || strings.TrimSpace(def.ID) == "" {
		return
	}
	if len(def.AllowedUsers) > 0 && strings.TrimSpace(def.Owner) != "" {
		orchestrateBaseDB.Set(sharedMachinesTable, def.ID, def.Owner)
		return
	}
	orchestrateBaseDB.Unset(sharedMachinesTable, def.ID)
}

// dropMachineShareIndex clears a deleted machine's entry.
func dropMachineShareIndex(id string) {
	if orchestrateBaseDB == nil || strings.TrimSpace(id) == "" {
		return
	}
	orchestrateBaseDB.Unset(sharedMachinesTable, id)
}

// userCanRunSharedMachine reports whether reqUser (not the owner) may use a
// shared machine: the machine lists them. The owner always can through their
// own store, so this is consulted only for a non-owner.
//
// The ONE place the question is answered — every surface that could hand a
// machine to somebody who does not own it calls this, so a future surface that
// forgets is a missing call rather than a second, subtly different rule.
func userCanRunSharedMachine(def MachineDef, reqUser string) bool {
	if strings.TrimSpace(reqUser) == "" {
		return false
	}
	for _, u := range def.AllowedUsers {
		if u == reqUser {
			return true
		}
	}
	return false
}

// SharedMachine is one machine reachable by somebody who does not own it,
// carried with its owner because everything downstream needs both: the recipe
// comes from the owner's store, and the run happens as the requester.
type SharedMachine struct {
	Def   MachineDef
	Owner string
}

// sharedMachinesFor returns the machines OTHER users have shared with reqUser,
// each with its owner. The index bounds the walk to machines somebody actually
// shared, and the record's own recipient list decides the rest.
func sharedMachinesFor(reqUser string) []SharedMachine {
	if orchestrateBaseDB == nil || strings.TrimSpace(reqUser) == "" {
		return nil
	}
	var out []SharedMachine
	for _, id := range orchestrateBaseDB.Keys(sharedMachinesTable) {
		var owner string
		if !orchestrateBaseDB.Get(sharedMachinesTable, id, &owner) || owner == "" || owner == reqUser {
			continue
		}
		def, ok := LoadMachineDef(UserDB(orchestrateBaseDB, owner), owner, id)
		if !ok || !userCanRunSharedMachine(def, reqUser) {
			continue
		}
		out = append(out, SharedMachine{Def: def, Owner: owner})
	}
	return out
}

// resolveMachineFor finds a machine for a requester: their own first, then one
// shared with them. Returns the def, its owner, and whether the requester owns
// it — callers gate writes on that last value rather than re-deriving it.
//
// Own-first is deliberate and matches resolvePipelineFor: a requester's own
// record shadows a foreign shared one with the same id, so nothing another user
// does can change what an id means in your own store.
func resolveMachineFor(reqUser string, udb Database, id string) (def MachineDef, owner string, mine bool, found bool) {
	if def, ok := LoadMachineDef(udb, reqUser, id); ok {
		return def, reqUser, true, true
	}
	for _, sm := range sharedMachinesFor(reqUser) {
		if sm.Def.ID == id {
			return sm.Def, sm.Owner, false, true
		}
	}
	return MachineDef{}, "", false, false
}

// machineForUser resolves a machine that an UNATTENDED run should fire for a
// user: their own (by name or id, which is what a schedule stores), else one
// shared with them. The schedule twin of resolveMachineFor, without an HTTP
// request to hang it on.
func machineForUser(user, ref string) (MachineDef, bool) {
	if strings.TrimSpace(user) == "" || strings.TrimSpace(ref) == "" {
		return MachineDef{}, false
	}
	if def, ok := findMachineByNameOrID(UserDB(orchestrateBaseDB, user), user, ref); ok {
		return def, true
	}
	// A shared machine answers to its name too. A schedule armed against one
	// stores whatever the picker gave it, and a recipient who can only reach
	// somebody else's machine by id would find the same schedule works for
	// their own machines and not for a shared one.
	for _, sm := range sharedMachinesFor(user) {
		if sm.Def.ID == ref || strings.EqualFold(strings.TrimSpace(sm.Def.Name), strings.TrimSpace(ref)) {
			return sm.Def, true
		}
	}
	return MachineDef{}, false
}

// machineMissingReason explains why a machine a schedule points at could not be
// resolved, in the words somebody needs to fix it.
//
// Deleted and un-shared are the same silence to the resolver and completely
// different problems to the reader: one means repoint the schedule, the other
// means ask a colleague. Worth a full walk because it runs only on the failure
// path of a schedule that is already broken.
func machineMissingReason(user, ref string) string {
	if orchestrateBaseDB != nil {
		for _, u := range AuthListUsers(orchestrateBaseDB) {
			if u.Username == user {
				continue
			}
			if def, ok := findMachineByNameOrID(UserDB(orchestrateBaseDB, u.Username), u.Username, ref); ok {
				return "machine " + strconv.Quote(def.Name) + " still exists, but " + u.Username + " no longer shares it with you"
			}
		}
	}
	return "no machine " + strconv.Quote(ref) + " — it was deleted or renamed, or the share was withdrawn"
}

// breakMachineSchedulesForLostRecipients marks broken any schedule belonging to
// a user who has just been taken off a machine's recipient list.
//
// A schedule must not outlive its permission. Without this, revoking a share
// leaves the recipient's schedule looking healthy and firing into a refusal at
// whatever hour it is armed for. Paused-and-kept, like every other broken
// dependency, so the owner of the schedule decides whether to repoint or delete
// it.
func breakMachineSchedulesForLostRecipients(def MachineDef, before []string) {
	still := map[string]bool{}
	for _, u := range def.AllowedUsers {
		still[u] = true
	}
	label := strings.TrimSpace(def.Name)
	if label == "" {
		label = def.ID
	}
	for _, u := range before {
		if u == "" || still[u] {
			continue
		}
		for _, sa := range ListStandingAgents(RootDB, u) {
			if !machineScheduleTargets(sa, def) {
				continue
			}
			MarkStandingAgentBroken(RootDB, u, sa.Name,
				fmt.Sprintf("runs machine %q, which %s no longer shares with you", label, def.Owner))
			Log("[orchestrate.machines] share of %q withdrawn from %q — their schedule %q was marked broken", label, u, sa.Name)
		}
	}
}

// machineScheduleTargets reports whether a schedule fires this machine. A
// schedule stores whatever the picker gave it, which is an id for one that was
// picked and a name for one that was typed, so both have to match or a revoke
// would leave half the schedules armed.
func machineScheduleTargets(sa StandingAgent, def MachineDef) bool {
	ref := strings.TrimSpace(sa.MachineID)
	if ref == "" {
		return false
	}
	return ref == def.ID || strings.EqualFold(ref, strings.TrimSpace(def.Name))
}

// listUserOwnedMachinesForAdmin walks every user's store and returns the
// machines they own, each with its recipient list.
//
// Deliberately NOT read from the share index: an audit that only sees what the
// index knows cannot show an unshared machine, and cannot reveal an index that
// has drifted from the records.
func listUserOwnedMachinesForAdmin(db Database) []UserOwnedMachineRow {
	out := []UserOwnedMachineRow{}
	if db == nil {
		return out
	}
	for _, u := range AuthListUsers(db) {
		udb := UserDB(db, u.Username)
		for _, d := range ListMachineDefs(udb, u.Username) {
			if d.Owner != u.Username || d.Owner == "" {
				continue
			}
			out = append(out, UserOwnedMachineRow{
				ID:         d.ID,
				Owner:      d.Owner,
				Name:       d.Name,
				SharedWith: strings.Join(d.AllowedUsers, ", "),
				Shared:     len(d.AllowedUsers) > 0,
				Steps:      len(d.Phases),
				Unattended: d.Unattended,
			})
		}
	}
	return out
}

// revokeMachineShareForAdmin clears a machine's recipient list. The owner keeps
// the machine; everybody else loses it. Saved through SaveMachineDef so the
// share index follows the record rather than needing its own revoke.
func revokeMachineShareForAdmin(db Database, owner, id string) error {
	if db == nil || owner == "" || id == "" {
		return fmt.Errorf("owner and id required")
	}
	udb := UserDB(db, owner)
	def, ok := LoadMachineDef(udb, owner, id)
	if !ok {
		return fmt.Errorf("no machine %q owned by %q", id, owner)
	}
	before := def.AllowedUsers
	def.Owner = owner
	def.AllowedUsers = nil
	SaveMachineDef(udb, def)
	breakMachineSchedulesForLostRecipients(def, before)
	Log("[orchestrate.machines] admin revoked every share of machine %q (owner=%q)", def.Name, owner)
	return nil
}
