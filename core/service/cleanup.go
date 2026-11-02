package service

// Clearing old connection records: what the rework left stored that nothing
// reads any more, removed by hand after reconfiguring (Admin > Maintenance >
// Reclaim space), never at start. A dry run lists exactly what the delete
// removes, by the same walk.
//
// Three kinds of record:
//
//   - a connection gohort once provided (plan B's LLM providers) whose spec
//     no longer validates and that no page references, with its own space
//     and any key in it. One that validates (a local server's) is an
//     ordinary connection now and stays: the embeddings one names the space
//     the stored vectors are in.
//   - a connection space no connector owns: settings and keys left behind.
//   - the marker of a migration that no longer exists.
//   - a saved flow: flows (connector chains) were retired, and nothing reads
//     them.
//
// The delete refuses while any setting needs setting again (StaleSettings):
// the keys plan B kept in its provided connections are what setting the LLM
// tiers again replaces, and until then they are the only copy.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
)

func init() {
	RegisterMaintenanceFunc("Reclaim space",
		"survey_old_connection_records",
		"Survey old connection records (dry run)",
		"Read-only. Lists the connections gohort once provided that no longer work and no page uses, "+
			"connection settings and keys no connection owns, the markers of migrations that are gone, and saved flows (retired). Deletes nothing.",
		func(ctx context.Context) int {
			old := findOldRecords(RootDB)
			if old.count() == 0 {
				Log("[connections] no old connection records")
				return 0
			}
			Log("[connections] old connection records:\n%s", strings.Join(old.lines(), "\n"))
			if stale := StaleSettings(RootDB); len(stale) > 0 {
				Log("[connections] the delete waits until these are set again: %s", stalePages(stale))
			}
			return old.count()
		},
	)
	RegisterMaintenanceFunc("Reclaim space",
		"clear_old_connection_records",
		"Clear old connection records (DELETES)",
		"Removes exactly what the dry run above lists. Refused while any setting needs setting again, "+
			"since the keys these records hold are replaced by setting it.",
		func(ctx context.Context) int {
			n, err := clearOldRecords(RootDB)
			if err != nil {
				Log("[connections] %v", err)
				ReportMaintenanceOutcome(ctx, err.Error())
			}
			return n
		},
	)
}

// oldMarkers are the markers of the migrations the rework removed.
var oldMarkers = []string{"connections:v2", "connections:v3", "images:v1", "source_hooks_to_connectors:v1",
	"search_research_to_sources:v1", "connectors_in_use:v1", "unfreeze_rest_image_defaults:v1"}

// flowsTable held the flows (connector chains), retired.
const flowsTable = "service_flows"

// oldRecords is what clearing removes: connection names, the ids of spaces
// no connection owns, marker keys, and the names of saved flows.
type oldRecords struct {
	connections, spaces, markers, flows []string
}

func (o oldRecords) count() int {
	return len(o.connections) + len(o.spaces) + len(o.markers) + len(o.flows)
}

// lines are o as the log says it, one record a line.
func (o oldRecords) lines() []string {
	var out []string
	for _, n := range o.connections {
		out = append(out, "connection "+n+" (once provided; its spec no longer validates and no page uses it), with its settings and key")
	}
	for _, id := range o.spaces {
		out = append(out, "settings and keys of "+id+", which no connection owns")
	}
	for _, k := range o.markers {
		out = append(out, "migration marker "+k)
	}
	for _, f := range o.flows {
		out = append(out, "saved flow "+f+" (flows are retired)")
	}
	return out
}

// providedMark reads the mark a connection gohort provided was stored with,
// which the connector record no longer carries.
type providedMark struct {
	Provided bool `json:"provided"`
}

// findOldRecords walks db for what clearing removes.
func findOldRecords(db Database) oldRecords {
	var o oldRecords
	if db == nil {
		return o
	}
	uses := TieIns(db)
	owned := map[string]bool{}
	for _, c := range ListConnectors(db) {
		owned[c.ID] = true
		var mark providedMark
		if !db.Get("connectors", c.Name, &mark) || !mark.Provided {
			continue
		}
		if ValidateConnector(c) != nil && len(uses[c.Name]) == 0 {
			o.connections = append(o.connections, c.Name)
		}
	}
	seen := map[string]bool{}
	for _, t := range db.AllTables() {
		space, _, ok := strings.Cut(t, "\x1f")
		id, isSpace := strings.CutPrefix(space, "connection:")
		if !ok || !isSpace || id == "" || owned[id] || seen[id] {
			continue
		}
		seen[id] = true
		o.spaces = append(o.spaces, id)
	}
	for _, name := range oldMarkers {
		key := migrate.MigrationMarker{App: "core", Name: name}.Key()
		var m migrate.MigrationMarker
		if db.Get(migrate.MigrationsTable, key, &m) {
			o.markers = append(o.markers, key)
		}
	}
	o.flows = db.Keys(flowsTable)
	sort.Strings(o.connections)
	sort.Strings(o.spaces)
	sort.Strings(o.flows)
	return o
}

// clearOldRecords removes what findOldRecords finds, saying each in the log,
// and how many it removed; it refuses while any setting needs setting again.
func clearOldRecords(db Database) (int, error) {
	if stale := StaleSettings(db); len(stale) > 0 {
		return 0, fmt.Errorf("refused: these need setting again first, since setting them replaces the keys these records hold: %s", stalePages(stale))
	}
	o := findOldRecords(db)
	for _, n := range o.connections {
		if err := DeleteConnector(db, n); err != nil {
			return 0, fmt.Errorf("removing %s: %w", n, err)
		}
	}
	for _, id := range o.spaces {
		prefix := "connection:" + id + "\x1f"
		for _, t := range db.AllTables() {
			if strings.HasPrefix(t, prefix) {
				db.Drop(t)
			}
		}
	}
	for _, k := range o.markers {
		db.Unset(migrate.MigrationsTable, k)
	}
	if len(o.flows) > 0 {
		db.Drop(flowsTable)
	}
	if o.count() > 0 {
		Log("[connections] removed:\n%s", strings.Join(o.lines(), "\n"))
	}
	return o.count(), nil
}

// stalePages are the pages of stale, each once, as a list.
func stalePages(stale []StaleSetting) string {
	var pages []string
	for _, st := range stale {
		if len(pages) == 0 || pages[len(pages)-1] != st.Page {
			pages = append(pages, st.Page)
		}
	}
	return strings.Join(pages, ", ")
}
