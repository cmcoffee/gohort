// The Sources picker: attach cross-app REFERENCE SOURCES to an agent
// (core.ReferenceSource) from the chat toolbar.
//
//	GET  /api/reference-sources?agent=<id> → {items: [...], attached: [id]}
//	POST /api/reference-sources?agent=<id>   {references: [id]}
//
// Until this existed the field was reachable only through the agent CRUD
// tool — you asked Builder to attach a source, or you did not attach one.
// That is a poor door for the field MOST likely to be wrong, because an
// attachment is the difference between an agent that can read your logs
// and one that cannot, and the failure is silent: the tools simply are
// not there and the agent says it does not know what you are talking
// about.
//
// It speaks the generic chip_picker (attach) contract in the
// dedicated-endpoint mode — attached_field + save_key — rather than
// pointing at the agent record. The record stores {kind,item_id}
// objects and a chip picker submits a flat list of scalars, so a
// record-mode picker would have to teach the shared component this
// domain's shape. Guides made the same call for the same reason; the
// composite handle is core's (ReferenceSelection.Ref / ParseReferenceRef)
// so the two cannot drift apart.
//
// The one thing this shows that guides' equivalent does not: the TOOL
// NAMES each attachment mints. An attached system is not an abstraction
// the agent absorbs — it is search_<name>_knowledge and
// investigate_<name> appearing in its catalog, and "I attached it but
// the agent has no idea what I mean" is what happens when nobody can see
// that list.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleReferenceSources drives the Sources modal.
func (T *OrchestrateApp) handleReferenceSources(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	if agentID == "" {
		http.Error(w, "agent required", http.StatusBadRequest)
		return
	}
	rec, found := loadAgent(udb, agentID)
	if !found {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		items := []map[string]any{}
		for _, grp := range ReferenceGroups(user) {
			for _, it := range grp.Items {
				row := map[string]any{
					"id":    ReferenceSelection{Kind: grp.Kind, ItemID: it.ID}.Ref(),
					"name":  it.Name,
					"desc":  refPickerDesc(user, grp.Kind, it),
					"group": grp.Label,
				}
				items = append(items, row)
			}
		}
		attached := []string{}
		for _, s := range rec.AttachedSources {
			attached = append(attached, s.Ref())
		}
		writeJSON(w, map[string]any{"items": items, "attached": attached})
	case http.MethodPost:
		var body struct {
			References []string `json:"references"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		sel := make(ReferenceSelections, 0, len(body.References))
		for _, ref := range body.References {
			if s, ok := ParseReferenceRef(ref); ok {
				sel = append(sel, s)
			}
		}
		// A selection is NOT validated against the live catalog. A source
		// can be temporarily absent (an app not yet registered this boot, a
		// disconnected MCP server) and dropping the attachment because the
		// producer is down would quietly rewrite the agent — the
		// broken-dependency posture is to keep the link and let the tools
		// be missing until it comes back.
		rec.AttachedSources = sel
		if _, err := saveAgent(udb, rec); err != nil {
			// saveAgent enforces the record's own invariants, so a save
			// CAN be refused here for a reason that has nothing to do with
			// sources. Reporting ok on a refused write is the failure that
			// looks exactly like a picker that does not persist.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[orchestrate.sources] user=%q agent=%q now attached to %d source(s)", user, rec.ID, len(sel))
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// refPickerDesc is the item's own description plus what attaching it
// actually DOES: the tools it mints on the agent.
//
// Named tools are the whole mechanism (see ReferenceToolProvider), and
// they are invisible everywhere else in the UI. Somebody who attaches a
// file store and then asks the agent to "check the logs in the log
// folder" has no way to learn the tool is called search_prod_logs unless
// this line says so.
func refPickerDesc(user, kind string, it ReferenceItem) string {
	desc := strings.TrimSpace(it.Desc)
	tools := ReferenceItemTools(user, kind, it.ID)
	if len(tools) == 0 {
		return desc
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Tool.Name)
	}
	line := "Adds: " + strings.Join(names, ", ")
	if desc == "" {
		return line
	}
	return desc + " · " + line
}
