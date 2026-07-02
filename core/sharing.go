// sharing.go — the storage + permission seam for SHARED records. A "shared
// record" is one a user owns (stored in that user's UserDB) that they've also
// published to every authenticated user. The owner's copy stays put; a global
// index in the app-wide store maps recordID -> owner username, so any request can
// discover it and operate it in the OWNER's context — one canonical place —
// regardless of who opened it. Presence in the index IS the shared flag; absence
// means private-to-owner.
//
// These are the generic primitives; each app keeps a thin resolve wrapper that
// loads its own record type from the owner's store (see apps/servitor/sharing.go
// and apps/guides — resolveGuide). Lifted here so servitor, guides, and any future
// app share ONE implementation instead of copy-pasting the index/permission logic.
package core

import "net/http"

// SetSharedOwner adds or removes a record from a shared index. shared && owner!=""
// registers it (recordID -> owner); anything else unregisters it. appDB is the
// app-wide store (NOT a per-user UserDB); indexTable is the app's chosen index
// table name (e.g. "shared_appliances", "shared_guides").
func SetSharedOwner(appDB Database, indexTable, id, owner string, shared bool) {
	if appDB == nil || id == "" {
		return
	}
	if shared && owner != "" {
		appDB.Set(indexTable, id, owner)
	} else {
		appDB.Unset(indexTable, id)
	}
}

// LookupSharedOwner returns the owner username for a shared record ID, and whether
// the ID is currently shared.
func LookupSharedOwner(appDB Database, indexTable, id string) (string, bool) {
	if appDB == nil || id == "" {
		return "", false
	}
	var owner string
	if appDB.Get(indexTable, id, &owner) && owner != "" {
		return owner, true
	}
	return "", false
}

// ListSharedOwners returns every shared record ID -> owner username in the index.
func ListSharedOwners(appDB Database, indexTable string) map[string]string {
	out := map[string]string{}
	if appDB == nil {
		return out
	}
	for _, id := range appDB.Keys(indexTable) {
		var owner string
		if appDB.Get(indexTable, id, &owner) && owner != "" {
			out[id] = owner
		}
	}
	return out
}

// RequestIsAdmin reports whether a request is from an admin. In a no-auth /
// single-user deployment (no users configured) everyone has full rights, matching
// the framework's UserHasAppAccess posture.
func RequestIsAdmin(r *http.Request) bool {
	if AuthDB == nil {
		return true
	}
	db := AuthDB()
	if db == nil || !AuthHasUsers(db) {
		return true
	}
	return AuthIsAdmin(db, r)
}

// CanManageShared reports whether reqUser may change sharing / edit / delete a
// record with the given owner: the owner, or an admin. Non-owners of a shared
// record can use it but not manage it. An empty owner is a legacy record with no
// owner stamp — its holder (it's in their store) manages it.
func CanManageShared(reqUser, owner string, isAdmin bool) bool {
	if isAdmin {
		return true
	}
	if owner == "" {
		return true
	}
	return owner == reqUser
}
