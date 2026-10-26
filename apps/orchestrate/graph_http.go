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
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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

// graphRoute lists an agent's graph: every entity with its outbound
// relationships + the entity/edge counts.
func (T *OrchestrateApp) graphRoute(at memAt) http.HandlerFunc {
	return jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
		t := at(c)
		udb := UserDB(T.DB, t.user)
		ns := factsNamespace(t.agentID)
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
		// How big it may grow, beside how big it is, so a graph near its cap
		// shows it.
		capEnt, capEdge := GraphCaps()
		return map[string]any{
			"counts":   map[string]int{"entities": ec, "edges": edc},
			"caps":     map[string]int{"entities": capEnt, "edges": capEdge},
			"entities": views,
		}, nil
	})
}

// graphEntityDeleteRoute removes one entity and all its edges.
func (T *OrchestrateApp) graphEntityDeleteRoute(at memAt) http.HandlerFunc {
	return T.graphDelete(at, func(udb Database, ns string, t memTarget, _ url.Values) (bool, error) {
		if strings.TrimSpace(t.item) == "" {
			return false, jsonapi.BadRequest("entity id required")
		}
		return DeleteGraphEntity(udb, ns, t.item), nil
	})
}

// graphDelete is a DELETE on the target's graph: 204 when del removed
// something, 404 when there was nothing to remove.
func (T *OrchestrateApp) graphDelete(at memAt, del func(udb Database, ns string, t memTarget, q url.Values) (bool, error)) http.HandlerFunc {
	return jsonapi.Delete(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
		t := at(c)
		removed, err := del(UserDB(T.DB, t.user), factsNamespace(t.agentID), t, c.R.URL.Query())
		if err != nil {
			return nil, err
		}
		if !removed {
			return nil, errNotFound
		}
		return jsonapi.Empty(http.StatusNoContent), nil
	})
}

// graphAttrDeleteRoute removes one attribute key from an entity.
//
//	DELETE /api/agents/{id}/graph/entity/{entity_id}/attr?key=orientation → 204
func (T *OrchestrateApp) graphAttrDeleteRoute(at memAt) http.HandlerFunc {
	return T.graphDelete(at, func(udb Database, ns string, t memTarget, q url.Values) (bool, error) {
		key := q.Get("key")
		if strings.TrimSpace(t.item) == "" || strings.TrimSpace(key) == "" {
			return false, jsonapi.BadRequest("entity id and key required")
		}
		return DeleteGraphEntityAttr(udb, ns, t.item, key), nil
	})
}

// graphAliasDeleteRoute removes one alias from an entity.
//
//	DELETE /api/agents/{id}/graph/entity/{entity_id}/alias?value=Robin → 204
func (T *OrchestrateApp) graphAliasDeleteRoute(at memAt) http.HandlerFunc {
	return T.graphDelete(at, func(udb Database, ns string, t memTarget, q url.Values) (bool, error) {
		val := q.Get("value")
		if strings.TrimSpace(t.item) == "" || strings.TrimSpace(val) == "" {
			return false, jsonapi.BadRequest("entity id and value required")
		}
		return DeleteGraphEntityAlias(udb, ns, t.item, val), nil
	})
}

// graphEdgeDeleteRoute removes one edge identified by from/rel/to query
// params (the values the GET view carries on each edge).
func (T *OrchestrateApp) graphEdgeDeleteRoute(at memAt) http.HandlerFunc {
	return T.graphDelete(at, func(udb Database, ns string, _ memTarget, q url.Values) (bool, error) {
		from, rel, to := q.Get("from"), q.Get("rel"), q.Get("to")
		if from == "" || rel == "" || to == "" {
			return false, jsonapi.BadRequest("from, rel, and to required")
		}
		return DeleteGraphEdge(udb, ns, from, rel, to), nil
	})
}
