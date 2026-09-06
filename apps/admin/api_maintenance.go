package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerMaintenanceRoutes wires the maintenance API under the admin sub-mux.
func (a *AdminApp) registerMaintenanceRoutes(sub *http.ServeMux) {
	// Vector Index snapshot — backs the admin DisplayPanel (page.go).
	sub.HandleFunc("/api/vector-stats", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleVectorStats(w, r)
	})

	// Per-kind breakdown (research / debate / lcm / collections / …) with doc +
	// chunk counts — the legible view of what's in the index, vs the opaque
	// per-source id dump.
	sub.HandleFunc("/api/vector-stats/by-kind", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleVectorStatsByKind(w, r)
	})

	// List registered maintenance functions (GET) or run one by key (POST ?key=<key>).
	sub.HandleFunc("/api/maintenance", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ListMaintenanceFuncs())
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}
		count := RunMaintenanceFunc(r.Context(), key)
		if count < 0 {
			http.Error(w, "unknown maintenance function", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"fixed": count})
	})

	// List every recorded migration marker across all apps + owners.
	// Read-only; markers are written by MigrationRunner.Once when each
	// migration fires. Operators clear markers manually (delete the row
	// from the DB) to force a re-run after fixing a panic.
	sub.HandleFunc("/api/migrations", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
	})

	// Scheduled tasks: list pending tasks (GET) or delete by ID (DELETE ?id=xxx).
	sub.HandleFunc("/api/scheduled-tasks", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			// Enrich each row with a human label (monitor name + agent, etc.)
			// via the task-describer registry, so the table can show WHAT a task
			// is rather than a bare kind + uuid. Kinds without a describer get an
			// empty detail (the id + kind still render).
			type schedTaskRow struct {
				ScheduledTask
				Detail string `json:"detail"`
			}
			tasks := ListScheduledTasks("")
			rows := make([]schedTaskRow, 0, len(tasks))
			for _, t := range tasks {
				rows = append(rows, schedTaskRow{ScheduledTask: t, Detail: DescribeTask(t)})
			}
			json.NewEncoder(w).Encode(rows)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		UnscheduleTask(id)
		w.WriteHeader(http.StatusNoContent)
	})

	// API: database browser.
	sub.HandleFunc("/api/db/tables", a.handleDBTables)

	sub.HandleFunc("/api/db/keys", a.handleDBKeys)

	sub.HandleFunc("/api/db/record", a.handleDBRecord)

}

// handleVectorStats returns a snapshot of the semantic-search index for the
// admin Vector Index panel: total chunks, how many carry an embedding, how
// many failed to embed and where, and a per-source breakdown.
//
// The walk lives in core.VectorStats rather than here. This handler used to
// carry its own copy of it, which left core's version with no callers — so the
// EmptyBySource breakdown, added there, would have been born dead. One walk,
// one definition of what "empty" means.
func (a *AdminApp) handleVectorStats(w http.ResponseWriter, r *http.Request) {
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
		emptyText = "none — every chunk has a vector"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total":                stats.Total,
		"embedded":             stats.Embedded,
		"empty":                stats.Empty,
		"by_source_text":       byText,
		"empty_by_source_text": emptyText,
	})
}

// handleVectorStatsByKind aggregates the index by SOURCE KIND — the prefix
// before the first ':' (research / debate / answer / lcm / collections / …) —
// reporting a document count (distinct full sources) and a chunk count per kind.
// The raw per-source ids are opaque; grouping by kind is what's actually legible.
func (a *AdminApp) handleVectorStatsByKind(w http.ResponseWriter, r *http.Request) {
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
		for _, k := range db.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !db.Get(EmbeddedChunks, k, &c) {
				continue
			}
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
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

func (a *AdminApp) handleDBTables(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tables := a.db.Tables()
	sort.Strings(tables)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tables)
}

func (a *AdminApp) handleDBKeys(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	table := r.URL.Query().Get("table")
	if table == "" {
		http.Error(w, "table required", http.StatusBadRequest)
		return
	}
	keys := a.db.Keys(table)
	sort.Strings(keys)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(keys)
}

func (a *AdminApp) handleDBRecord(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	table := r.URL.Query().Get("table")
	key := r.URL.Query().Get("key")
	if table == "" || key == "" {
		http.Error(w, "table and key required", http.StatusBadRequest)
		return
	}

	// DBase.Get calls Critical(err) on decode failure, which kills the server.
	// Bypass the wrapper by accessing the underlying kvlite.Store directly so
	// we can probe multiple concrete types without a fatal on type mismatch.
	dbase, ok := a.db.(*DBase)
	if !ok {
		http.Error(w, "unsupported database type", http.StatusInternalServerError)
		return
	}

	val, found := dbProbeRecord(dbase.Store, table, key)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	b, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		http.Error(w, "marshal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// dbProbeRecord tries to decode a kvlite record into the first matching
// primitive type. For complex/struct values it returns a descriptive
// placeholder. Uses Store.Get directly to avoid the Critical(err) wrapper.
func dbProbeRecord(store interface {
	Get(table, key string, output interface{}) (bool, error)
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
		found, err := store.Get(table, key, ptr)
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
