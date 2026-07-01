package enginseer

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// mapRepoPrompt drives a proactive full-repo mapping run: walk the structure and
// record it into the graph so later questions start from a real map.
const mapRepoPrompt = "Map this repository's architecture. Orient yourself with list_dir and search_code, then trace the main structure: the top-level packages/modules, their dependencies (what imports/calls what), the entry points, and the major data stores or services. Record everything you establish with link_entities (one relationship per call, each component its own entity with its file path in subject_attrs) so the map persists. Be systematic but don't exhaustively read every file — capture the shape of the system. When you've mapped the primary structure, give a short plain-text summary of the architecture."

// handleMapRepo kicks a background mapping investigation for the selected repo.
// It runs headless via RunScopedAgent (the investigator + the repo's read/search
// + link_entities tools, scoped to the repo), building the code graph. The user
// watches the result appear under Memory → Graph Memory. Returns immediately.
func (T *Enginseer) handleMapRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		RepoID string `json:"repo_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	rec, found := loadRepo(udb, req.RepoID)
	if !found {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate unavailable", http.StatusServiceUnavailable)
		return
	}
	if rec.Files == 0 {
		http.Error(w, "repository has no ingested files yet — wait for the clone to finish, or Refresh", http.StatusConflict)
		return
	}
	tools := repoTools(user, rec.ID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		scope := orchestrate.AgentScope{AgentID: repoInvestigatorAgentID, ScopeUser: repoScope(rec.ID)}
		if _, err := orch.RunScopedAgent(ctx, scope, mapRepoPrompt, nil, tools...); err != nil {
			Log("[enginseer] map run failed for %s: %v", rec.Name, err)
		} else {
			Log("[enginseer] map run complete for %s", rec.Name)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}
