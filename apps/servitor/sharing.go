// sharing.go — shared appliances/repos. An appliance is owned by the user who
// created it and stored in that user's UserDB. When Shared is set it's ALSO
// listed in a global index (in T.DB, the app-wide store) mapping its ID to the
// owner, so any authenticated user can discover and operate it in the owner's
// context — same creds, same repo clone, same scoped memory — while keeping
// their OWN chat sessions. This is the storage seam for that; the handlers use
// resolveAppliance in place of a bare per-user udb.Get.
package servitor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/peershare"
)

// servitorIsAdmin reports whether the request is from an admin. Thin wrapper over
// the generic core.RequestIsAdmin.
func servitorIsAdmin(r *http.Request) bool { return RequestIsAdmin(r) }

// sharedIndexTable lives in the app-global store (T.DB, NOT a per-user UserDB)
// and maps a shared appliance/repo ID -> its owner username. Presence IS the
// shared flag; absence means private-to-owner.
const sharedIndexTable = "shared_appliances"

// setApplianceShared adds or removes an appliance from the global shared index.
func (T *Servitor) setApplianceShared(applianceID, owner string, shared bool) {
	SetSharedOwner(T.DB, sharedIndexTable, applianceID, owner, shared)
}

// sharedOwner returns the owner username of a shared appliance ID, and whether
// the ID is currently shared.
func (T *Servitor) sharedOwner(applianceID string) (string, bool) {
	return LookupSharedOwner(T.DB, sharedIndexTable, applianceID)
}

// namedSharedTable indexes systems shared with named people or teams:
// recipient -> (owner, system id). Derived from Appliance.AllowedUsers, and
// re-checked against it on every read.
const namedSharedTable = "named_shared_systems"

// indexNamedShares makes the index follow the record's AllowedUsers.
func (T *Servitor) indexNamedShares(owner, id string, users []string) {
	peershare.SetRecipients(T.DB, namedSharedTable, owner, id, users)
}

// systemsSharedWith is every system somebody else shares with user, id to
// owner: with everyone, or with them by name or through a team.
func (T *Servitor) systemsSharedWith(user string) map[string]string {
	out := map[string]string{}
	for id, owner := range T.listSharedAppliances() {
		if owner != user {
			out[id] = owner
		}
	}
	for _, ref := range SharedRefsFor(T.DB, namedSharedTable, user) {
		if ref.Owner == user {
			continue
		}
		if udb := UserDB(T.DB, ref.Owner); udb != nil {
			var a Appliance
			if udb.Get(applianceTable, ref.ID, &a) && SharedWithUser(a.AllowedUsers, ref.Owner, user) {
				out[ref.ID] = ref.Owner
			}
		}
	}
	return out
}

// reaches reports whether a system somebody else owns is shared with user.
func (T *Servitor) reaches(user, id string) bool {
	_, ok := T.systemsSharedWith(user)[id]
	return ok
}

// fullAccessFor reports whether user may reach the live system: run commands
// on it, with the owner's login. The owner may. Somebody it is shared with
// may when they hold Full access, or when the owner has not saved levels yet
// (LevelsSet), or through the old shared-with-everyone, which kept its full
// use. Everyone else it reaches has Read.
func (a Appliance) fullAccessFor(owner, user string) bool {
	switch {
	case user == "" || owner == "":
		return false
	case user == owner:
		return true
	case a.Shared:
		return true
	case !a.LevelsSet:
		return SharedWithUser(a.AllowedUsers, owner, user)
	}
	return SharedWithUser(a.FullAccess, owner, user)
}

// listSharedAppliances returns every shared appliance ID -> owner username.
func (T *Servitor) listSharedAppliances() map[string]string {
	return ListSharedOwners(T.DB, sharedIndexTable)
}

// resolveAppliance finds the appliance for a request: the requesting user's OWN
// store first, else via the shared index (the owner's store). Returns the
// record, the owner username, the owner's UserDB (use this for appliance-context
// operations — docs, repo files, scoped memory — so a shared record operates in
// ONE place regardless of who opened it), and whether it was found. For a record
// the user owns, ownerUser == reqUser and ownerUDB == reqUDB, so non-shared flows
// are unchanged. Chat SESSIONS must still use the requesting user's own udb.
func (T *Servitor) resolveAppliance(reqUser string, reqUDB Database, applianceID string) (Appliance, string, Database, bool) {
	var a Appliance
	if reqUDB != nil && reqUDB.Get(applianceTable, applianceID, &a) {
		owner := a.Owner
		if owner == "" {
			owner = reqUser // legacy record without an Owner stamp: the holder owns it
		}
		return a, owner, reqUDB, true
	}
	if owner, ok := T.systemsSharedWith(reqUser)[applianceID]; ok {
		if ownerUDB := UserDB(T.DB, owner); ownerUDB != nil && ownerUDB.Get(applianceTable, applianceID, &a) {
			return a, owner, ownerUDB, true
		}
	}
	return Appliance{}, "", nil, false
}

// localCommandAllowed refuses a LOCAL command-type appliance whose owner is
// not an admin. That type runs `sh -c` on the gohort host itself, as the
// gohort process, so owning one is owning the server and every tenant's data
// on it. Creation is admin-only (web_appliances.go); this is the runtime half,
// which also stops any such record saved before that gate existed. A remote
// stub (PeerName set) executes on the peer, under the peer's own policy.
func localCommandAllowed(a Appliance) error {
	if a.Type != "command" || strings.TrimSpace(a.PeerName) != "" {
		return nil
	}
	if UserIsAdmin(a.Owner) {
		return nil
	}
	return fmt.Errorf("%s is a local command system owned by a non-admin account; it runs commands on the gohort server itself, so only an admin-owned one may run", applianceLabel(a.Name, a.ID))
}

// EnvVars on a local command appliance are how its owner hands the command a
// token, a password, an API key. Sharing the appliance shares the ability to
// USE the command, not the secrets it runs with: the record another user sees
// carries the variable names only, and output from a session they run has the
// values replaced before it reaches them.

// redactEnvVars returns env with every value removed, keeping the names so a
// viewer can still see what the command is configured with.
func redactEnvVars(env []string) []string {
	if len(env) == 0 {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		out = append(out, name)
	}
	return out
}

// restoreRedactedEnvVars puts the stored value back on any entry that arrives
// as a bare name - the redacted form - so a record round-tripped through a
// redacted view can never blank a secret.
func restoreRedactedEnvVars(in, stored []string) []string {
	if len(in) == 0 {
		return in
	}
	have := map[string]string{}
	for _, kv := range stored {
		if name, _, ok := strings.Cut(kv, "="); ok {
			have[name] = kv
		}
	}
	out := make([]string, 0, len(in))
	for _, kv := range in {
		if !strings.Contains(kv, "=") {
			if full, ok := have[strings.TrimSpace(kv)]; ok {
				out = append(out, full)
			}
			continue // a bare name with nothing stored behind it means nothing
		}
		out = append(out, kv)
	}
	return out
}

// minScrubLen: values shorter than this ("1", "on") are not secrets and would
// shred ordinary output if replaced.
const minScrubLen = 4

// scrubEnvValues replaces every EnvVars value in out. Best effort by nature:
// a user who can run arbitrary commands with the variables set can still get
// at them by transforming them first. It closes the plain reads - env,
// printenv, echo $VAR, an error message that quotes one - which is what a
// shared user would otherwise see without trying.
func scrubEnvValues(out string, env []string) string {
	for _, kv := range env {
		_, v, ok := strings.Cut(kv, "=")
		if !ok || len(v) < minScrubLen {
			continue
		}
		out = strings.ReplaceAll(out, v, "[hidden]")
	}
	return out
}

// envHiddenFrom reports whether user runs a's command without being its owner,
// and so must not see its EnvVars values. owner is the resolved owner (the
// record's Owner is empty on legacy records).
func envHiddenFrom(a Appliance, owner, user string) bool {
	if owner == "" {
		owner = a.Owner
	}
	return len(a.EnvVars) > 0 && owner != "" && owner != user
}

// canManageAppliance reports whether reqUser may change sharing / edit / delete
// the record: the owner, or an admin. Non-owners of a shared record can use it
// but not manage it. Thin wrapper over the generic core.CanManageShared.
func canManageAppliance(reqUser string, a Appliance, isAdmin bool) bool {
	return CanManageShared(reqUser, a.Owner, isAdmin)
}

// keptEveryone is whether a save leaves a system shared with everyone.
//
// A system is never newly shared with everyone. Whoever it is shared with
// runs it as the owner (the same login, the same repository token), so
// Everyone would hand every account in the deployment, and every one made
// later, the owner's access to that machine. Name the people or the team.
//
// A system shared with everyone before stays so until its owner takes it
// out: the list reads Everyone back, and a save that keeps it keeps it. A
// save that drops it, or a list sent without it, ends it, and nothing turns
// it on: not Everyone in the list, not the old shared:true.
func keptEveryone(prevShared bool, list []string, legacyShared bool) bool {
	if !prevShared {
		return false
	}
	if list == nil {
		return legacyShared // a body from before the list said it this way
	}
	for _, u := range list {
		if strings.TrimSpace(u) == ShareEveryone {
			return true
		}
	}
	return false
}

// cleanSystemRecipients trims a recipient list and drops the owner, repeats
// and Everyone, which is only ever the kept Shared flag (keptEveryone).
func cleanSystemRecipients(users []string, owner string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range users {
		if u = strings.TrimSpace(u); u != "" && u != owner && u != ShareEveryone && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// readOnlyRefusal is why user may not reach the system live, or "" when they
// may: for a door that runs on the system whatever the request says (a map).
func readOnlyRefusal(a Appliance, owner, user string) string {
	if owner == "" || owner == user || a.fullAccessFor(owner, user) {
		return ""
	}
	return applianceLabel(a.Name, a.ID) + " is shared with you to read: mapping runs commands on it. Ask " + owner + " for Full access, or ask questions to answer from what has been gathered."
}

// keepReached is the entries of list that reach is also listing.
func keepReached(list, reach []string) []string {
	in := map[string]bool{}
	for _, u := range reach {
		in[u] = true
	}
	var out []string
	for _, u := range list {
		if in[u] {
			out = append(out, u)
		}
	}
	return out
}

// handleUserCandidates serves the share picker's people and teams. Never
// Everyone: see keptEveryone.
func (T *Servitor) handleUserCandidates(c *jsonapi.Call) (any, error) {
	return json.RawMessage(ShareCandidatesJSON(c.User, c.R.URL.Query().Get("groups") == "1", false)), nil
}
