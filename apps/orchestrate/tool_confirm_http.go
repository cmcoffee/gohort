// The ask-before-every-call flag, read and written from the Tools modal.
//
// Sits beside admin_tool_scope.go's /api/tool-scope on purpose: both edit a
// property of the TOOL RECORD from a per-agent modal, so both have to say out
// loud that the change lands everywhere the tool is held. Scope answers "where
// is this available"; this answers "does it stop and ask first".
//
// The console Permissions page writes the same flag through
// handleConsolePermissionPolicy's "confirmtool:" kind. Two routes, one setter:
// both call core.SetUserToolConfirmInChat and neither derives the state, which
// is the trap the earlier per-tool ladder fell into.
package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleToolConfirm serves the Ask control in the Tools modal.
//
// GET returns every persistent tool the user owns as name to flag. The modal
// needs the whole map, not just the tools that ask: a name being absent is how
// it knows a catalog row is a framework tool rather than one of the user's,
// and a framework tool has no record to carry the flag.
//
// POST {"name":…,"on":…} flips one.
func (T *OrchestrateApp) handleToolConfirm(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		tools := map[string]bool{}
		agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
		asks := map[string]bool{}
		for _, n := range AskInChatTools(AuthDB(), user, agentID) {
			asks[n] = true
		}
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			tools[p.Tool.Name] = asks[p.Tool.Name]
		}
		w.Header().Set("Content-Type", "application/json")
		// No caching, for the reason /api/tool-scope gives: the modal re-reads
		// right after a write and a stale body snaps the control back.
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"tools": tools})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
			On   bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		// Scoped to this user's own pool by construction: the setter is handed
		// the requesting account, so a name belonging to somebody else simply
		// is not found rather than being edited.
		// Agent-scoped, like every other permission. The mark is keyed by
		// name so it needs no record, which is what lets a framework tool
		// carry one too.
		if !SetUserToolAsksInChat(AuthDB(), user, strings.TrimSpace(r.URL.Query().Get("agent")), body.Name, body.On) {
			http.Error(w, "could not record that", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
