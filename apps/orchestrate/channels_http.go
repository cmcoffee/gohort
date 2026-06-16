package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// handleChannels manages an agent's attached messaging Channels. Phase 1 is
// data model + attach only — a binding here records WHICH agent owns a
// messaging surface, but nothing routes inbound to the agent yet (that's
// Phase 2). See docs/channels-and-agents.md. Owner-scoped; stored in RootDB
// alongside the other fleet data.
//
//	GET    /api/channels?agent_id=<id>  → list this owner's channels (filtered
//	                                       to the agent when agent_id is set)
//	POST   /api/channels?agent_id=<id>  → create a channel bound to the agent
//	DELETE /api/channels?id=<id>        → detach (delete) a channel
func (T *OrchestrateApp) handleChannels(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		out := []Channel{}
		for _, ch := range ListChannels(RootDB, user) {
			if agentID != "" && ch.AgentID != agentID {
				continue
			}
			out = append(out, ch)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)

	case http.MethodPost:
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			http.Error(w, "agent_id is required", http.StatusBadRequest)
			return
		}
		var req struct {
			Name      string `json:"name"`
			Service   string `json:"service"`
			Address   string `json:"address"`
			AutoReply bool   `json:"auto_reply"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Service) == "" {
			http.Error(w, "service is required", http.StatusBadRequest)
			return
		}
		ch := Channel{
			ID:        NewChannelID(),
			Owner:     user,
			Name:      strings.TrimSpace(req.Name),
			Service:   strings.TrimSpace(req.Service),
			Address:   strings.TrimSpace(req.Address),
			AgentID:   agentID,
			AutoReply: req.AutoReply,
			Created:   time.Now().UTC().Format(time.RFC3339),
		}
		SaveChannel(RootDB, ch)
		Log("[orchestrate.channels] user=%q attached channel %q (service=%s scope=%q) to agent=%s",
			user, ch.Name, ch.Service, ch.Address, agentID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ch)

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		DeleteChannel(RootDB, user, id)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
