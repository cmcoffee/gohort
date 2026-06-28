// AppSpec is a stored, data-driven app: a Page (the client-shaped pageConfig
// JSON the runtime renders) plus a per-app record store key. It lives in core so
// BOTH the host that serves it (apps/customapps) and the authoring tool that
// writes it (the app_def Builder tool in apps/orchestrate) can reach the type +
// its storage without importing each other. The Page bytes are built once from
// ui.Page types (by whoever authors the spec) and stored verbatim — core itself
// needs no ui dependency here, only json.RawMessage.
//
// IMPORTANT — specs live in a SHARED, deployment-root store keyed by owner, NOT
// in either app's per-app bucket. Each app's AppCore.DB is global.db.Bucket(app
// name), so a spec written through orchestrate's DB would be invisible to
// customapps' DB and vice-versa. Routing all spec storage through RootDB (the
// bucket-less deployment root) under user:<owner> gives both apps one place to
// read and write, so an app authored by app_def actually shows up + serves in
// customapps.
package core

import (
	"encoding/json"
	"time"
)

// AppSpecTable is the per-user kvlite table holding AppSpecs, keyed by slug.
const AppSpecTable = "app_specs"

// AppSpec is one data-driven app. Page holds the pageConfig JSON (from
// ui.Page.ConfigJSON) served verbatim — no Go Component round-trip. RecordKey is
// the primary-key field of the per-app record store. AgentID optionally binds an
// agent that powers the app's chat surface.
type AppSpec struct {
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	Desc      string          `json:"desc"`
	Owner     string          `json:"owner"`
	AgentID   string          `json:"agent_id,omitempty"`
	Page      json.RawMessage `json:"page"`
	RecordKey string          `json:"record_key"`
	// BodyField is the record field a workbench's viewer renders + its co-author
	// tool appends to (the document body). Empty for non-workbench apps.
	BodyField string `json:"body_field,omitempty"`
	Created   string `json:"created"`
	Updated   string `json:"updated"`
}

// appSpecStore returns the shared per-owner spec store (RootDB → user:<owner>),
// or nil when RootDB isn't wired yet / owner is empty. Both the authoring tool
// and the host resolve the store here, so they always agree regardless of which
// app's DB bucket they happen to hold.
func appSpecStore(owner string) Database {
	if RootDB == nil || owner == "" {
		return nil
	}
	return UserDB(RootDB, owner)
}

// LoadAppSpec reads one spec by slug for an owner.
func LoadAppSpec(owner, slug string) (AppSpec, bool) {
	db := appSpecStore(owner)
	if db == nil {
		return AppSpec{}, false
	}
	var s AppSpec
	ok := db.Get(AppSpecTable, slug, &s)
	return s, ok
}

// SaveAppSpec writes a spec, stamping Owner/Created/Updated. Owner on the spec
// wins; pass it set. No-op return when RootDB isn't available.
func SaveAppSpec(s AppSpec) AppSpec {
	db := appSpecStore(s.Owner)
	if db == nil {
		return s
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if s.Created == "" {
		s.Created = now
	}
	s.Updated = now
	db.Set(AppSpecTable, s.Slug, s)
	return s
}

// ListAppSpecs returns every stored spec owned by the user.
func ListAppSpecs(owner string) []AppSpec {
	db := appSpecStore(owner)
	if db == nil {
		return nil
	}
	var out []AppSpec
	for _, k := range db.Keys(AppSpecTable) {
		var s AppSpec
		if db.Get(AppSpecTable, k, &s) {
			out = append(out, s)
		}
	}
	return out
}

// DeleteAppSpec removes a spec by slug for an owner.
func DeleteAppSpec(owner, slug string) {
	if db := appSpecStore(owner); db != nil {
		db.Unset(AppSpecTable, slug)
	}
}
