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

// UserOwnedAgentRow is one user-owned agent for the admin governance console (the
// user-plane audit). Populated by orchestrate via AdminListUserOwnedAgents so the
// admin app needn't import orchestrate. SharedWith is the recipient list joined
// for display; Shared mirrors len(recipients) > 0.
type UserOwnedAgentRow struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	SharedWith string `json:"shared_with,omitempty"`
	Shared     bool   `json:"shared"`
}

// AdminListUserOwnedAgents / AdminRevokeAgentShare are wired by orchestrate (in an
// init) so the admin governance console can enumerate and un-share user-owned
// agents across the deployment without the admin app importing orchestrate. They
// take the app-wide Database (admin passes its own) and mirror the
// AdminRehomeOrphanTool hook pattern. Nil until orchestrate's init runs (single
// binary, so always set in practice).
var (
	AdminListUserOwnedAgents func(db Database) []UserOwnedAgentRow
	AdminRevokeAgentShare    func(db Database, owner, id string) error
)

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
