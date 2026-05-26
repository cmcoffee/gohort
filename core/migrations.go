// One-shot data migration helpers. Apps with stored state that
// evolves over time use these to fire schema/data fixups exactly
// once per (app, name, owner) tuple, regardless of how often the
// trigger handler is invoked.
//
// Pattern:
//
//	func myAppMigrations(owner string) {
//	    m := core.NewMigrationRunner("myapp", owner)
//	    m.Once("rename_field:v1", func() int { ... })
//	    m.Once("backfill_default:v1", func() int { ... })
//	}
//
// Markers persist in a single deployment-wide table (MigrationsTable
// in RootDB) keyed by "<app>:<name>:<owner>". Each marker carries
// when the migration ran, how many records it changed, and any error
// captured at run time. The admin UI surfaces every recorded marker
// so operators can see the migration state of the deployment at a
// glance — "what has fired, when, and how many records moved."
//
// Compared to the original boolean-only marker:
//   - Single central table instead of per-app — one place to inspect.
//   - Timestamps + record counts so operators can audit drift.
//   - Panic capture so a failed migration is visible (not silently
//     marked done with no clue why nothing happened).

package core

import (
	"fmt"
	"time"
)

// MigrationsTable is the deployment-wide table that stores marker
// records for every migration run. Lives in RootDB so a single
// admin endpoint can scan across all apps without per-app table
// lookups.
const MigrationsTable = "migrations"

// MigrationMarker is the persisted record for one migration run.
// Written by MigrationRunner.Once after fn returns; read by the
// admin UI to surface migration history.
type MigrationMarker struct {
	App     string    `json:"app"`               // app namespace (e.g. "orchestrate")
	Name    string    `json:"name"`              // migration name with version (e.g. "rename_attach_file:v1")
	Owner   string    `json:"owner,omitempty"`   // per-user scope; empty = deployment-wide
	Done    bool      `json:"done"`              // true once the migration has completed (success OR failed)
	RanAt   time.Time `json:"ran_at,omitempty"`  // when the migration body fired
	Changed int       `json:"changed,omitempty"` // record count returned by fn
	Error   string    `json:"error,omitempty"`   // captured panic, if any
}

// Key returns the canonical storage key for a marker: app:name:owner.
// Empty owner produces "app:name:" so deployment-wide markers have a
// stable, unique key.
func (m MigrationMarker) Key() string {
	return m.App + ":" + m.Name + ":" + m.Owner
}

// MigrationRunner gates one-shot data fixups against the marker table.
// Each migration runs at most once per (app, name, owner) tuple; the
// marker is stored after fn returns. Re-running the trigger (e.g. on
// every request that calls runMigrations) is cheap — already-done
// migrations short-circuit on a single DB read of the marker.
type MigrationRunner struct {
	app   string
	owner string
}

// NewMigrationRunner wires a runner under the given app namespace and
// per-user scope. Owner is appended to each marker's storage key so
// the same migration runs once per user (the typical shape for apps
// with per-user data). Pass empty owner for migrations scoped to the
// deployment rather than a single user.
//
// Migrations always write to RootDB.MigrationsTable regardless of
// caller; app-scoping happens via the key, not via storage location.
func NewMigrationRunner(app, owner string) *MigrationRunner {
	return &MigrationRunner{app: app, owner: owner}
}

// Once runs fn at most once for (app, name, owner). fn returns the
// count of records changed; non-zero counts get logged as a one-liner.
// Silent no-op when the marker is already set OR when RootDB is unset
// (defensive — keeps init-order surprises from panicking).
//
// Panics inside fn are recovered, captured into the marker's Error
// field, and the marker is still written with Done=true so a broken
// migration doesn't loop forever on every trigger. Operators see the
// error in the admin migrations table and can fix-then-rerun by
// deleting that marker manually.
func (m *MigrationRunner) Once(name string, fn func() int) {
	if m == nil || RootDB == nil || name == "" {
		return
	}
	marker := MigrationMarker{App: m.app, Name: name, Owner: m.owner}
	var existing MigrationMarker
	if RootDB.Get(MigrationsTable, marker.Key(), &existing) && existing.Done {
		return
	}
	started := time.Now()
	changed, panicErr := runMigrationFunc(fn)
	marker.Done = true
	marker.RanAt = started
	marker.Changed = changed
	if panicErr != "" {
		marker.Error = panicErr
	}
	RootDB.Set(MigrationsTable, marker.Key(), marker)
	if panicErr != "" {
		Log("[migration] %s/%s PANIC for %s: %s", m.app, name, ownerLabel(m.owner), panicErr)
		return
	}
	if changed > 0 {
		Log("[migration] %s/%s: %d record(s) changed for %s", m.app, name, changed, ownerLabel(m.owner))
	}
}

// runMigrationFunc invokes fn with a recover so a panic doesn't take
// down the calling goroutine. Returns the change count fn produced
// (or 0 if it panicked before returning) and the recovered error as
// a string (empty on success).
func runMigrationFunc(fn func() int) (changed int, panicErr string) {
	defer func() {
		if r := recover(); r != nil {
			panicErr = fmt.Sprintf("%v", r)
		}
	}()
	changed = fn()
	return
}

func ownerLabel(owner string) string {
	if owner == "" {
		return "(global)"
	}
	return owner
}

// ListMigrationMarkers returns every recorded migration marker across
// every app + owner. Used by the admin Migrations section to surface
// "what has fired on this deployment, when, and how many records."
// Empty when RootDB is unset or no migrations have ever run.
func ListMigrationMarkers() []MigrationMarker {
	if RootDB == nil {
		return nil
	}
	keys := RootDB.Keys(MigrationsTable)
	out := make([]MigrationMarker, 0, len(keys))
	for _, k := range keys {
		var m MigrationMarker
		if RootDB.Get(MigrationsTable, k, &m) {
			out = append(out, m)
		}
	}
	return out
}
