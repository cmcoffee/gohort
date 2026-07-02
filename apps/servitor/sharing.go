// sharing.go — shared appliances/repos. An appliance is owned by the user who
// created it and stored in that user's UserDB. When Shared is set it's ALSO
// listed in a global index (in T.DB, the app-wide store) mapping its ID to the
// owner, so any authenticated user can discover and operate it in the owner's
// context — same creds, same repo clone, same scoped memory — while keeping
// their OWN chat sessions. This is the storage seam for that; the handlers use
// resolveAppliance in place of a bare per-user udb.Get.
package servitor

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
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
	if owner, ok := T.sharedOwner(applianceID); ok {
		if ownerUDB := UserDB(T.DB, owner); ownerUDB != nil && ownerUDB.Get(applianceTable, applianceID, &a) {
			return a, owner, ownerUDB, true
		}
	}
	return Appliance{}, "", nil, false
}

// canManageAppliance reports whether reqUser may change sharing / edit / delete
// the record: the owner, or an admin. Non-owners of a shared record can use it
// but not manage it. Thin wrapper over the generic core.CanManageShared.
func canManageAppliance(reqUser string, a Appliance, isAdmin bool) bool {
	return CanManageShared(reqUser, a.Owner, isAdmin)
}
