package phantom

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// handleMigrateToChannel bootstraps a Channels setup from phantom's existing
// persona: it mints an orchestrate agent carrying the phantom persona (system
// prompt + enabled tools) and a whole-service iMessage Channel bound to it, so
// inbound iMessage routes to that orchestrate agent instead of phantom's own
// engine. The phantom config is left intact — coexistence means unbound chats
// keep using phantom until you widen the binding, so this is safe to run and
// verify before fully cutting over.
//
// Per-chat persona overrides are NOT migrated here: in the channel model a
// different persona is just a different agent, so each override becomes its
// own agent + a per-contact Channel (Address = that handle, which overrides
// the whole-service default). That's a follow-on. See
// docs/channels-and-agents.md.
//
//	POST /phantom/api/migrate-channel  → {agent_id, channel_id}
func (T *Phantom) handleMigrateToChannel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	owner := phantomToolOwner(T.DB)
	if owner == "" || owner != user {
		http.Error(w, "only the phantom owner can migrate", http.StatusForbidden)
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate runtime not available", http.StatusServiceUnavailable)
		return
	}
	cfg := defaultConfig(T.DB)

	// Persona → agent prompt: Personality (who it is) prepended to SystemPrompt
	// (the rules), matching how phantom assembles its own prompt. Phantom-only
	// formatting rules (emoji/case/SMS length) are dropped — the channel agent
	// gets per-service delivery formatting from the ServicePolicy instead.
	prompt := strings.TrimSpace(strings.TrimSpace(cfg.Personality) + "\n\n" + strings.TrimSpace(cfg.SystemPrompt))
	if prompt == "" {
		prompt = "You are a helpful assistant answering messages on iMessage. Keep replies short and conversational."
	}
	name := strings.TrimSpace(cfg.PersonaName)
	if name == "" {
		name = "iMessage agent"
	}
	rec := orchestrate.AgentRecord{
		Name:               name,
		Description:        "Migrated from the phantom persona; answers on iMessage.",
		OrchestratorPrompt: prompt,
		AllowedTools:       cfg.EnabledTools,
		// Channel agent posture: no Fleet, no Cortex.
	}
	agentID, err := orch.SaveAgentForUser(owner, rec)
	if err != nil {
		http.Error(w, "create agent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ch := Channel{
		ID:        NewChannelID(),
		Owner:     owner,
		Name:      name + " (iMessage)",
		Service:   phantomDefaultService, // "imessage"
		Address:   "",                    // whole-service binding
		AgentID:   agentID,
		AutoReply: true,
		Created:   time.Now().UTC().Format(time.RFC3339),
	}
	SaveChannel(RootDB, ch)
	Log("[phantom] migrated persona %q -> orchestrate agent %s + iMessage channel %s", name, agentID, ch.ID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"agent_id": agentID, "channel_id": ch.ID})
}
