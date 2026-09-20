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
	Exposed    bool   `json:"exposed"` // already published as a /agents/ app
}

// AdminListUserOwnedAgents / AdminRevokeAgentShare / AdminPublishAgent are wired by
// orchestrate (in an init) so the admin governance console can enumerate, un-share,
// and DELEGATE (publish) user-owned agents across the deployment without the admin
// app importing orchestrate. They take the app-wide Database (admin passes its own)
// and mirror the AdminRehomeOrphanTool hook pattern. Nil until orchestrate's init
// runs (single binary, so always set in practice). Publish is the admin's top-down
// "delegate to users" step: it flips the agent Exposed so it becomes a /agents/
// app the admin can then grant to users (the owner's peer-share stays independent).
var (
	AdminListUserOwnedAgents func(db Database) []UserOwnedAgentRow
	AdminRevokeAgentShare    func(db Database, owner, id string) error
	AdminPublishAgent        func(db Database, owner, id string) error
)

// UserOwnedPipelineRow is one user-owned pipeline for the same governance
// console. Deliberately the AGENT row minus Exposed: a pipeline has no published
// app surface to flip, so a field for it would be a control that does nothing.
type UserOwnedPipelineRow struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	SharedWith string `json:"shared_with,omitempty"`
	Shared     bool   `json:"shared"`
	Stages     int    `json:"stages"`
}

// AdminListUserOwnedPipelines / AdminRevokePipelineShare are the pipeline half of
// the pair above, wired by orchestrate in the same init. An admin who can audit
// and revoke a shared agent must be able to do both for a shared pipeline, or
// half the user plane is governable and the other half is invisible.
var (
	AdminListUserOwnedPipelines func(db Database) []UserOwnedPipelineRow
	AdminRevokePipelineShare    func(db Database, owner, id string) error
)

// UserOwnedMachineRow is one user-owned machine for the same governance
// console. The pipeline row with Steps for Stages, plus Unattended: whether a
// machine RUNS or converses is what decides where a share can be spent — a
// recipient can put an unattended one on a timetable or hand it to an agent,
// while a conversational one is only ever reached by a person talking to it.
type UserOwnedMachineRow struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	SharedWith string `json:"shared_with,omitempty"`
	Shared     bool   `json:"shared"`
	Steps      int    `json:"steps"`
	Unattended bool   `json:"unattended"`
}

// AdminListUserOwnedMachines / AdminRevokeMachineShare are the machine half of
// the same pair, wired by orchestrate in the same init. Three shareable kinds
// now exist in the user plane, and an admin who can audit two of them has a
// console that is quietly wrong about the third.
var (
	AdminListUserOwnedMachines func(db Database) []UserOwnedMachineRow
	AdminRevokeMachineShare    func(db Database, owner, id string) error
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
