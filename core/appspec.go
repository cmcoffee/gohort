// AppSpec is a stored, data-driven app: a Page (the client-shaped pageConfig
// JSON the runtime renders) plus a per-app record store key. It lives in core so
// BOTH the host that serves it (apps/customapps) and the authoring tool that
// writes it (the app_def Builder tool in apps/orchestrate) can reach the type +
// its storage without importing each other. The Page bytes are built once from
// ui.Page types (by whoever authors the spec) and stored verbatim — core itself
// needs no ui dependency here, only json.RawMessage.
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
	Created   string          `json:"created"`
	Updated   string          `json:"updated"`
}

// LoadAppSpec reads one spec by slug from the user's db.
func LoadAppSpec(db Database, slug string) (AppSpec, bool) {
	var s AppSpec
	ok := db.Get(AppSpecTable, slug, &s)
	return s, ok
}

// SaveAppSpec writes a spec, stamping Updated (and Created on first write).
func SaveAppSpec(db Database, s AppSpec) AppSpec {
	now := time.Now().UTC().Format(time.RFC3339)
	if s.Created == "" {
		s.Created = now
	}
	s.Updated = now
	db.Set(AppSpecTable, s.Slug, s)
	return s
}

// ListAppSpecs returns every stored spec for the user.
func ListAppSpecs(db Database) []AppSpec {
	var out []AppSpec
	for _, k := range db.Keys(AppSpecTable) {
		if s, ok := LoadAppSpec(db, k); ok {
			out = append(out, s)
		}
	}
	return out
}

// DeleteAppSpec removes a spec by slug.
func DeleteAppSpec(db Database, slug string) { db.Unset(AppSpecTable, slug) }
