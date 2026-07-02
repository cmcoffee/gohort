// Admin-UI HTTP surface for an agent's graph memory — the read-only + delete
// view behind the Memory button's "Graph" section. Mirrors the Reference
// Memory (inferred) endpoints: GET lists, DELETE prunes. Editing the graph
// stays the LLM's job via link_entities; this is for inspection and cleanup.
//
//	GET    /api/agents/{id}/graph                      → {counts, entities[]}
//	DELETE /api/agents/{id}/graph/entity/{entity_id}   → 204 (entity + its edges)
//	DELETE /api/agents/{id}/graph/edge?from=&rel=&to=  → 204 (one edge)

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

type graphEdgeView struct {
	Rel    string `json:"rel"`     // display form ("works at")
	To     string `json:"to"`      // target entity ID (for delete)
	ToName string `json:"to_name"` // target display name
	Note   string `json:"note,omitempty"`
}

type graphEntityView struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Name    string            `json:"name"`
	Aliases []string          `json:"aliases,omitempty"`
	Attrs   map[string]string `json:"attrs,omitempty"`
	Edges   []graphEdgeView   `json:"edges,omitempty"` // outbound only (each relationship listed once, under its subject)
}

// handleAgentGraph lists an agent's graph: every entity with its outbound
// relationships + the entity/edge counts.
func (T *OrchestrateApp) handleAgentGraph(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	udb := UserDB(T.DB, user)
	ns := factsNamespace(agentID)
	ents := ListGraphEntities(udb, ns)
	views := make([]graphEntityView, 0, len(ents))
	for _, e := range ents {
		ev := graphEntityView{ID: e.ID, Kind: e.Kind, Name: e.Name, Aliases: e.Aliases, Attrs: e.Attrs}
		for _, ed := range GraphEdgesFrom(udb, ns, e.ID) {
			ev.Edges = append(ev.Edges, graphEdgeView{
				Rel:    strings.ReplaceAll(ed.Rel, "_", " "),
				To:     ed.To,
				ToName: graphEndName(udb, ns, ed.To),
				Note:   ed.Note,
			})
		}
		views = append(views, ev)
	}
	ec, edc := GraphCounts(udb, ns)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"counts":   map[string]int{"entities": ec, "edges": edc},
		"entities": views,
	})
}

// handleAgentGraphEntityDelete removes one entity and all its edges.
func (T *OrchestrateApp) handleAgentGraphEntityDelete(w http.ResponseWriter, r *http.Request, user, agentID, entityID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(entityID) == "" {
		http.Error(w, "entity id required", http.StatusBadRequest)
		return
	}
	if DeleteGraphEntity(UserDB(T.DB, user), factsNamespace(agentID), entityID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

// handleAgentGraphAttrDelete removes one attribute key from an entity.
//
//	DELETE /api/agents/{id}/graph/entity/{entity_id}/attr?key=orientation → 204
func (T *OrchestrateApp) handleAgentGraphAttrDelete(w http.ResponseWriter, r *http.Request, user, agentID, entityID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	key := r.URL.Query().Get("key")
	if strings.TrimSpace(entityID) == "" || strings.TrimSpace(key) == "" {
		http.Error(w, "entity id and key required", http.StatusBadRequest)
		return
	}
	if DeleteGraphEntityAttr(UserDB(T.DB, user), factsNamespace(agentID), entityID, key) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

// handleAgentGraphAliasDelete removes one alias from an entity.
//
//	DELETE /api/agents/{id}/graph/entity/{entity_id}/alias?value=Robin → 204
func (T *OrchestrateApp) handleAgentGraphAliasDelete(w http.ResponseWriter, r *http.Request, user, agentID, entityID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	val := r.URL.Query().Get("value")
	if strings.TrimSpace(entityID) == "" || strings.TrimSpace(val) == "" {
		http.Error(w, "entity id and value required", http.StatusBadRequest)
		return
	}
	if DeleteGraphEntityAlias(UserDB(T.DB, user), factsNamespace(agentID), entityID, val) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

// handleAgentGraphEdgeDelete removes one edge identified by from/rel/to query
// params (the values the GET view carries on each edge).
func (T *OrchestrateApp) handleAgentGraphEdgeDelete(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	from, rel, to := q.Get("from"), q.Get("rel"), q.Get("to")
	if from == "" || rel == "" || to == "" {
		http.Error(w, "from, rel, and to required", http.StatusBadRequest)
		return
	}
	if DeleteGraphEdge(UserDB(T.DB, user), factsNamespace(agentID), from, rel, to) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}
