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
		// Carry a brand-correct service label (imessage → iMessage) alongside
		// each channel so the UI shows it without hardcoding service names.
		type channelView struct {
			Channel
			ServiceLabel string `json:"service_label,omitempty"`
			// ManageOnly: this channel relays into its agent's cortex (a DEDICATED
			// cortex agent — Cortex on + exactly one channel), so it has no
			// per-channel thread of its own. The rail makes its row open the manage
			// dialog instead of an (empty) thread; the conversation is read in the
			// cortex hero. Multi-channel / non-cortex agents keep per-room threads.
			ManageOnly bool `json:"manage_only,omitempty"`
		}
		udb := UserDB(T.DB, user)
		out := []channelView{}
		for _, ch := range ListChannels(RootDB, user) {
			if agentID != "" && ch.AgentID != agentID {
				continue
			}
			manage := false
			if ag, ok := loadAgent(udb, ch.AgentID); ok && ag.Cortex &&
				len(ListChannelsForAgent(RootDB, user, ch.AgentID)) == 1 {
				manage = true
			}
			out = append(out, channelView{Channel: ch, ServiceLabel: ServiceDisplayName(ch.Service), ManageOnly: manage})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)

	case http.MethodPost:
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		var req struct {
			ID          string `json:"id"` // set → update in place; empty → create
			Name        string `json:"name"`
			Description string `json:"description"`
			Service     string `json:"service"`
			Address     string `json:"address"`
			AgentID     string `json:"agent_id"` // optional re-point on update / create from body
			AutoReply   bool   `json:"auto_reply"`
			Direction   string `json:"direction"`
			Gatekeeper  string `json:"gatekeeper"`
			TagOverride string `json:"tag_override"` // per-channel outbound name-tag override ("" = inherit)
			TagDisabled bool   `json:"tag_disabled"` // disable the outbound name tag on this channel
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Update in place when an id is given: preserve service/address/created,
		// refresh the editable fields, allow re-pointing the bound agent.
		if id := strings.TrimSpace(req.ID); id != "" {
			ch, found := GetChannel(RootDB, user, id)
			if !found {
				http.Error(w, "channel not found", http.StatusNotFound)
				return
			}
			// Specifying a channel is just the interface fields; the connector
			// (service + address) is attached separately, so it's preserved here
			// rather than wiped by an absent value.
			ch.Name = strings.TrimSpace(req.Name)
			ch.Description = strings.TrimSpace(req.Description)
			ch.AutoReply = req.AutoReply
			ch.Direction = strings.TrimSpace(req.Direction)
			ch.Gatekeeper = strings.TrimSpace(req.Gatekeeper)
			ch.TagOverride = strings.TrimSpace(req.TagOverride)
			ch.TagDisabled = req.TagDisabled
			if a := strings.TrimSpace(req.AgentID); a != "" {
				ch.AgentID = a
				T.ensureAgentCortex(user, a) // re-pointed: the new agent gets a cortex too
			}
			SaveChannel(RootDB, ch)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ch)
			return
		}
		// Create — agent from the query (rail is agent-scoped) or the body.
		if agentID == "" {
			agentID = strings.TrimSpace(req.AgentID)
		}
		if agentID == "" {
			http.Error(w, "agent_id is required", http.StatusBadRequest)
			return
		}
		// Service may be blank: an inert channel — the interface is configured
		// but no source is hooked in yet, so nothing routes until one is.
		ch := Channel{
			ID:          NewChannelID(),
			Owner:       user,
			Name:        strings.TrimSpace(req.Name),
			Description: strings.TrimSpace(req.Description),
			Service:     strings.TrimSpace(req.Service),
			Address:     strings.TrimSpace(req.Address),
			AgentID:     agentID,
			AutoReply:   req.AutoReply,
			Direction:   strings.TrimSpace(req.Direction),
			Gatekeeper:  strings.TrimSpace(req.Gatekeeper),
			TagOverride: strings.TrimSpace(req.TagOverride),
			TagDisabled: req.TagDisabled,
			Created:     time.Now().UTC().Format(time.RFC3339),
		}
		SaveChannel(RootDB, ch)
		// A channel relays into its agent's cortex (the conversation thread), so
		// attaching one implies cortex — turn it on if it isn't already.
		T.ensureAgentCortex(user, agentID)
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

// ensureAgentCortex turns Cortex ON for the agent a channel was just attached to.
// A channel is a relay into its agent's cortex (the conversation thread) — a
// channel on a cortex-less agent has nowhere to relay, so attaching one implies
// cortex. Idempotent: no-op when already on or the agent can't be loaded. For a
// seed agent this writes the per-user shadow the same way any editor save does.
func (T *OrchestrateApp) ensureAgentCortex(user, agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		return
	}
	a, ok := loadAgent(udb, agentID)
	if !ok || a.Cortex {
		return
	}
	a.Cortex = true
	if _, err := saveAgent(udb, a); err != nil {
		Log("[orchestrate.channels] enable cortex on agent %s failed: %v", agentID, err)
	}
}
