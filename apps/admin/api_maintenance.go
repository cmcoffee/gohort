package admin

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerMaintenanceRoutes wires the maintenance API under the admin sub-mux.
func (a *AdminApp) registerMaintenanceRoutes(sub *http.ServeMux) {
	// Vector Index snapshot — backs the admin DisplayPanel (page.go).
	sub.HandleFunc("/api/vector-stats", jsonapi.Get(a.db, a.handleVectorStats, a.gate()))

	// Per-kind breakdown (research / debate / lcm / collections / …) with doc +
	// chunk counts — the legible view of what's in the index, vs the opaque
	// per-source id dump.
	sub.HandleFunc("/api/vector-stats/by-kind", jsonapi.Get(a.db, a.handleVectorStatsByKind, a.gate()))

	// Where a running pass has got to. Polled by the action list while its
	// POST is in flight, so a long pass shows movement rather than a button
	// that has been disabled for four minutes.
	sub.HandleFunc("/api/maintenance/progress", jsonapi.Get(a.db, a.handleMaintenanceProgress, a.gate()))

	// Stop a running pass. It ends at its next check of its context and its
	// outcome reads "stopped"; the press that started it gets its count back
	// as usual.
	sub.HandleFunc("/api/maintenance/cancel", jsonapi.Methods{
		"POST": jsonapi.Post(a.db, a.handleMaintenanceCancel, a.gate()),
	}.Serve)

	// List registered maintenance functions (GET) or run one by key (POST ?key=<key>).
	sub.HandleFunc("/api/maintenance", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleMaintenanceList, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleMaintenanceRun, a.gate()),
	}.Serve)

	// List every recorded migration marker across all apps + owners.
	// Read-only; markers are written by MigrationRunner.Once when each
	// migration fires. Operators clear markers manually (delete the row
	// from the DB) to force a re-run after fixing a panic.
	sub.HandleFunc("/api/migrations", jsonapi.Get(a.db, a.handleMigrations, a.gate()))

	// Scheduled tasks: list pending tasks (GET) or delete by ID (DELETE ?id=xxx).
	sub.HandleFunc("/api/scheduled-tasks", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleScheduledTasks, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleScheduledTaskDelete, a.gate()),
	}.Serve)

	// API: database browser.
	sub.HandleFunc("/api/db/tables", jsonapi.Get(a.db, a.handleDBTables, a.gate()))

	sub.HandleFunc("/api/db/keys", jsonapi.Get(a.db, a.handleDBKeys, a.gate()))

	sub.HandleFunc("/api/db/record", jsonapi.Get(a.db, a.handleDBRecord, a.gate()))

}

// handleMaintenanceProgress is where the ?key= pass has got to, and how the
// last one ended. Two fields, because they answer different questions:
// progress is where a RUNNING pass has got to, outcome is how the last one
// ended. A page that arrives after a run it did not start has only the
// second, and showing nothing there reads as "it never ran".
func (a *AdminApp) handleMaintenanceProgress(c *jsonapi.Call) (any, error) {
	key := c.R.URL.Query().Get("key")
	return map[string]string{
		"progress": MaintenanceProgress(key),
		"outcome":  MaintenanceOutcome(key),
	}, nil
}

// handleMaintenanceCancel stops the ?key= pass, if one is running.
func (a *AdminApp) handleMaintenanceCancel(c *jsonapi.Call, _ struct{}) (any, error) {
	msg := "Nothing is running there."
	if CancelMaintenance(c.R.URL.Query().Get("key")) {
		msg = "Stopping: it ends at its next step."
	}
	return map[string]string{"message": msg}, nil
}

// handleMaintenanceList is the registered passes, each with how it went last
// time. ?group= narrows the list to one admin section. A registrant that
// named no group, or one the page does not lay out, is shown under
// Housekeeping so no button can vanish.
func (a *AdminApp) handleMaintenanceList(c *jsonapi.Call) (any, error) {
	want := c.R.URL.Query().Get("group")
	all := ListMaintenanceFuncs()
	out := all[:0:0]
	for _, f := range all {
		g := f.Group
		if !maintenanceGroupKnown(g) {
			g = maintenanceGroupOther
		}
		if want == "" || g == want {
			out = append(out, f)
		}
	}
	// Each item carries what happened to it last time. Built here
	// rather than in the registry because the record is admin's and so
	// is the identity in it.
	rows := make([]map[string]any, 0, len(out))
	for _, f := range out {
		run, ok := a.lastMaintenanceRun(f.Key)
		rows = append(rows, map[string]any{
			"Group": f.Group, "Key": f.Key, "Label": f.Label, "Desc": f.Desc,
			"History": maintenanceHistoryLine(run, ok),
		})
	}
	return rows, nil
}

// handleMaintenanceRun runs the ?key= pass to its end and answers its count.
func (a *AdminApp) handleMaintenanceRun(c *jsonapi.Call, _ struct{}) (any, error) {
	key := c.R.URL.Query().Get("key")
	if key == "" {
		return nil, jsonapi.BadRequest("missing key")
	}
	// The run outlives the request. A request context dies the moment the
	// browser does, and the passes check it every iteration — so closing
	// the tab used to stop a re-embed wherever it had got to, silently,
	// which is exactly what the progress spinner invites you to do.
	// WithoutCancel keeps the request's values (deadlines and identity
	// for anything downstream) and drops only its cancellation.
	started := time.Now()
	first, release := claimMaintenancePress(key)
	// Released in a defer: a pass that panics must not leave its key
	// claimed, or no later press would ever be recorded again.
	count := func() int {
		defer release()
		return RunMaintenanceFunc(context.WithoutCancel(c.R.Context()), key)
	}()
	if count < 0 {
		return nil, jsonapi.NotFound("unknown maintenance function")
	}
	// Recorded after it returns, so a pass that panicked leaves the
	// previous record standing rather than claiming a run that did not
	// finish. RunMaintenanceFunc blocks for the whole pass; the progress
	// endpoint is what the page watches meanwhile.
	// A press that joined one already running reports the count and
	// leaves the record to the press that started it.
	if first {
		a.recordMaintenanceRun(key, c.User, count, time.Since(started))
	}
	return map[string]int{"fixed": count}, nil
}

// handleMigrations is every recorded migration marker.
func (a *AdminApp) handleMigrations(c *jsonapi.Call) (any, error) {
	markers := ListMigrationMarkers()
	rows := make([]map[string]any, 0, len(markers))
	for _, m := range markers {
		owner := m.Owner
		if owner == "" {
			owner = "(global)"
		}
		var ranAt any
		if !m.RanAt.IsZero() {
			ranAt = m.RanAt
		}
		rows = append(rows, map[string]any{
			"key":     m.Key(),
			"app":     m.App,
			"name":    m.Name,
			"owner":   owner,
			"ran_at":  ranAt,
			"changed": m.Changed,
			"error":   m.Error,
		})
	}
	return rows, nil
}

// handleScheduledTasks is the pending scheduled tasks. Each row is enriched
// with a human label (monitor name + agent, etc.) via the task-describer
// registry, so the table can show WHAT a task is rather than a bare kind +
// uuid. Kinds without a describer get an empty detail (the id + kind still
// render).
func (a *AdminApp) handleScheduledTasks(c *jsonapi.Call) (any, error) {
	type schedTaskRow struct {
		ScheduledTask
		Detail string `json:"detail"`
	}
	tasks := ListScheduledTasks("")
	rows := make([]schedTaskRow, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, schedTaskRow{ScheduledTask: t, Detail: DescribeTask(t)})
	}
	return rows, nil
}

// handleScheduledTaskDelete unschedules the ?id= task.
func (a *AdminApp) handleScheduledTaskDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	id := c.R.URL.Query().Get("id")
	if id == "" {
		return nil, jsonapi.BadRequest("missing id")
	}
	UnscheduleTask(id)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleVectorStats returns a snapshot of the semantic-search index for the
// admin Vector Index panel: total chunks, how many carry an embedding, how
// many failed to embed and where, and a per-source breakdown.
//
// The walk lives in core.VectorStats rather than here. This handler used to
// carry its own copy of it, which left core's version with no callers — so the
// EmptyBySource breakdown, added there, would have been born dead. One walk,
// one definition of what "empty" means.
func (a *AdminApp) handleVectorStats(c *jsonapi.Call) (any, error) {
	db := VectorDB
	if db == nil {
		db = RootDB
	}
	stats := VectorStats(db)
	byText := stats.BySourceText
	if byText == "" {
		byText = "(no chunks yet)"
	}
	// "none" rather than "" so the row reads as a checked, healthy result
	// instead of a field that failed to load.
	emptyText := stats.EmptyBySourceText
	if emptyText == "" {
		emptyText = "none: every chunk has a vector"
	}
	staleText := stats.StaleBySourceText
	if staleText == "" {
		staleText = "none: every vector is in the current space"
	}
	unusableText := stats.UnusableBySourceText
	if unusableText == "" {
		unusableText = "none"
	}
	return map[string]any{
		"total":                   stats.Total,
		"embedded":                stats.Embedded,
		"empty":                   stats.Empty,
		"stale":                   stats.Stale,
		"unusable":                stats.Unusable,
		"by_source_text":          byText,
		"empty_by_source_text":    emptyText,
		"stale_by_source_text":    staleText,
		"unusable_by_source_text": unusableText,
	}, nil
}

// handleVectorStatsByKind aggregates the index by SOURCE KIND — the prefix
// before the first ':' (research / debate / answer / lcm / collections / …) —
// reporting a document count (distinct full sources) and a chunk count per kind.
// The raw per-source ids are opaque; grouping by kind is what's actually legible.
func (a *AdminApp) handleVectorStatsByKind(c *jsonapi.Call) (any, error) {
	db := VectorDB
	if db == nil {
		db = RootDB
	}
	type kindAgg struct {
		docs   map[string]bool
		chunks int
	}
	agg := map[string]*kindAgg{}
	if db != nil {
		for _, c := range ChunksWhere(db, func(EmbeddedChunk) bool { return true }) {
			src := c.Source
			if src == "" {
				src = "(unspecified)"
			}
			kind := src
			if i := strings.IndexByte(src, ':'); i >= 0 {
				kind = src[:i]
			}
			ka := agg[kind]
			if ka == nil {
				ka = &kindAgg{docs: map[string]bool{}}
				agg[kind] = ka
			}
			ka.docs[src] = true
			ka.chunks++
		}
	}
	rows := make([]map[string]any, 0, len(agg))
	for kind, ka := range agg {
		rows = append(rows, map[string]any{
			"kind":      kind,
			"label":     vectorKindLabel(kind),
			"documents": len(ka.docs),
			"chunks":    ka.chunks,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["chunks"].(int) > rows[j]["chunks"].(int) })
	return rows, nil
}

// vectorKindLabel maps a source-kind prefix to a human label; unknown kinds
// pass through unchanged.
func vectorKindLabel(kind string) string {
	switch strings.ToLower(kind) {
	case "research":
		return "Research reports"
	case "debate":
		return "Debates"
	case "answer":
		return "Answers"
	case "lcm":
		return "Conversation history"
	case "collection", "collections":
		return "Document collections"
	case "skill", "skills":
		return "Skills"
	case "hook", "source-hook", "sourcehook":
		return "Source hooks"
	case "(unspecified)":
		return "Unspecified"
	}
	return kind
}

func (a *AdminApp) handleDBTables(c *jsonapi.Call) (any, error) {
	tables := a.db.Tables()
	sort.Strings(tables)
	return tables, nil
}

func (a *AdminApp) handleDBKeys(c *jsonapi.Call) (any, error) {
	r := c.R
	table := r.URL.Query().Get("table")
	if table == "" {
		return nil, jsonapi.BadRequest("table required")
	}
	keys := a.db.Keys(table)
	sort.Strings(keys)
	return keys, nil
}

// handleDBRecord is one record of the database browser, compact: the page
// lays it out itself.
func (a *AdminApp) handleDBRecord(c *jsonapi.Call) (any, error) {
	table := c.R.URL.Query().Get("table")
	key := c.R.URL.Query().Get("key")
	if table == "" || key == "" {
		return nil, jsonapi.BadRequest("table and key required")
	}

	// Probing means guessing a type and being wrong most times. That used to
	// have to reach past the Database wrapper, because a decode failure inside
	// it called Critical and ended the process — a browser that could kill the
	// server by looking at the wrong key. TryGet reports the mismatch instead,
	// so this reads the framework's own store like everything else does.
	val, found := dbProbeRecord(a.db, table, key)
	if !found {
		return nil, jsonapi.NotFound("not found")
	}
	return val, nil
}

// dbProbeRecord tries to decode a kvlite record into the first matching
// primitive type. For complex/struct values it returns a descriptive
// placeholder. ProbeGet rather than Get or TryGet: a wrong guess has to come
// back as a mismatch to try the next type against, which Get would report as
// absent and stop on — and it must NOT be counted as a store failure, because
// guessing wrong is how this works. Counting it left the maintenance page
// reporting a failing database after one ordinary browse.
func dbProbeRecord(store interface {
	ProbeGet(table, key string, output interface{}) (bool, error)
}, table, key string) (interface{}, bool) {
	// Ordered by how commonly these appear in settings/routing/config tables.
	probes := []interface{}{
		new(string),
		new(bool),
		new(int),
		new(int64),
		new(float64),
		new([]string),
		new([]byte),
	}
	for _, ptr := range probes {
		found, err := store.ProbeGet(table, key, ptr)
		if !found {
			return nil, false
		}
		if err != nil {
			continue
		}
		// Dereference the pointer to get the concrete value.
		switch v := ptr.(type) {
		case *string:
			return *v, true
		case *bool:
			return *v, true
		case *int:
			return *v, true
		case *int64:
			return *v, true
		case *float64:
			return *v, true
		case *[]string:
			return *v, true
		case *[]byte:
			return *v, true
		}
	}
	// Value exists but is a struct type — return a placeholder rather than crashing.
	return map[string]string{"_type": "struct", "_note": "binary-encoded struct; map probe not supported"}, true
}

// maintenanceGroups are the admin sections maintenance buttons are laid out
// under, in page order. A registrant names one of these (MaintenanceFunc.Group);
// anything else is shown under the last.
// The groups the admin page lays out. A registrant naming anything else is
// folded into maintenanceGroupOther below, so no button can vanish — which is
// the right safety net and also a very quiet one: the function appears, just
// not where its author said to look.
//
// "Migrations" was missing here while a note told operators to find the
// guardrail sweep under Migrations. The net caught it and put it in
// Housekeeping, so it was reachable the whole time and the note sent people to
// the wrong section. Adding a maintenanceList for a group is only half of it;
// the name has to be known here too, or the new list asks for a group the
// endpoint has already renamed and renders empty.
var maintenanceGroups = []string{"Vector index", "Reclaim space", "Reports", "Housekeeping", "Migrations"}

const maintenanceGroupOther = "Housekeeping"

func maintenanceGroupKnown(g string) bool {
	for _, k := range maintenanceGroups {
		if k == g {
			return true
		}
	}
	return false
}
