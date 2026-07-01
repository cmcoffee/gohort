// Package appagents is the cross-app registry for "App Agents": any app can
// declare agents it owns (an investigator, a synthesizer, a domain persona) by
// calling RegisterAppAgent in its init(); orchestrate folds them into its agent
// resolution so they load, list, and accept a per-user customization shadow
// exactly like its own in-code seeds. The agent definition stays CODE/APP-
// DECLARED and read-mostly (a third tier between per-user agents and
// orchestrate's own seeds) — the deployment owns operational state (a user's
// Rules, approved tools) via the shadow overlay, but the prompt/structure is
// the app's.
//
// This is a pure leaf package: it depends on nothing in core (only the
// stdlib), so the owning app layer (orchestrate) maps AppAgentSpec → its own
// AgentRecord at resolution time. Keep this struct domain-agnostic.
//
// Extracted from core/ (was core.app_agents) as the first leaf seam off the
// core package — see the core dependency-graph map.
package appagents

import "sync"

// AppAgentSpec is one app-owned agent definition. ID must be stable and
// globally unique (convention: "app-<owningapp>-<role>"). Prompt becomes the
// agent's orchestrator/system prompt. Hidden keeps a secret-sauce role out of
// public-facing pickers (it still resolves for dispatch).
type AppAgentSpec struct {
	ID           string   `json:"id"`
	OwningApp    string   `json:"owning_app"` // DISPLAY label for the App Agents grouping — the app's own optgroup in the picker (e.g. "Custom Apps"). Falls back to "App Agents" when empty.
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Prompt       string   `json:"prompt"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Hidden       bool     `json:"hidden,omitempty"`
	Cortex       bool     `json:"cortex,omitempty"`
	// MemoryMode shapes the Explicit Memory layer: "agent" (narrow — generalized
	// lessons only, specifics go to Reference Memory) or "chatbot" (broad — adds
	// user personalization). Empty defaults to "agent". A task-focused app agent
	// (an investigator, a probe) should set "agent"; a conversational one "chatbot".
	MemoryMode string `json:"memory_mode,omitempty"`
	// DisableExplicit turns the Explicit Memory layer OFF entirely (no store_fact,
	// no always-in-prompt facts block, and the UI hides the "Saved facts" section).
	// Set it for an app agent that records only into other layers (servitor writes
	// facts to the graph and prose to Reference Memory, never Explicit Memory).
	DisableExplicit bool `json:"disable_explicit,omitempty"`
}

var (
	mu    sync.RWMutex
	specs = map[string]AppAgentSpec{}
	order []string // registration order, for stable display
)

// RegisterAppAgent adds (or replaces, by ID) an app-owned agent. Call once per
// agent from the owning app's init(). A blank ID is ignored.
func RegisterAppAgent(spec AppAgentSpec) {
	if spec.ID == "" {
		return
	}
	mu.Lock()
	if _, exists := specs[spec.ID]; !exists {
		order = append(order, spec.ID)
	}
	specs[spec.ID] = spec
	mu.Unlock()
}

// AppAgents returns every registered app agent in registration order.
func AppAgents() []AppAgentSpec {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]AppAgentSpec, 0, len(order))
	for _, id := range order {
		out = append(out, specs[id])
	}
	return out
}

// AppAgentByID looks up one registered app agent. Lets the resolver/dashboard
// tell an app-owned agent (and its owning app) apart from orchestrate's own
// seeds and per-user records.
func AppAgentByID(id string) (AppAgentSpec, bool) {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := specs[id]
	return s, ok
}
