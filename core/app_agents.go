// App-agent registry — the cross-app seam for "App Agents". Any app can
// declare agents it owns (an investigator, a synthesizer, a domain persona)
// by calling RegisterAppAgent in its init(); orchestrate folds them into its
// agent resolution so they load, list, and accept a per-user customization
// shadow exactly like its own in-code seeds. The agent definition stays
// CODE/APP-DECLARED and read-mostly (a third tier between per-user agents and
// orchestrate's own seeds) — the deployment owns operational state (a user's
// Rules, approved tools) via the shadow overlay, but the prompt/structure is
// the app's.
//
// AppAgentSpec is deliberately a MINIMAL, portable subset: core can't import
// orchestrate's AgentRecord, so the owning app layer (orchestrate) maps spec →
// record at resolution time. Keep this struct domain-agnostic.
package core

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
}

var (
	appAgentsMu    sync.RWMutex
	appAgentSpecs  = map[string]AppAgentSpec{}
	appAgentsOrder []string // registration order, for stable display
)

// RegisterAppAgent adds (or replaces, by ID) an app-owned agent. Call once per
// agent from the owning app's init(). A blank ID is ignored.
func RegisterAppAgent(spec AppAgentSpec) {
	if spec.ID == "" {
		return
	}
	appAgentsMu.Lock()
	if _, exists := appAgentSpecs[spec.ID]; !exists {
		appAgentsOrder = append(appAgentsOrder, spec.ID)
	}
	appAgentSpecs[spec.ID] = spec
	appAgentsMu.Unlock()
}

// AppAgents returns every registered app agent in registration order.
func AppAgents() []AppAgentSpec {
	appAgentsMu.RLock()
	defer appAgentsMu.RUnlock()
	out := make([]AppAgentSpec, 0, len(appAgentsOrder))
	for _, id := range appAgentsOrder {
		out = append(out, appAgentSpecs[id])
	}
	return out
}

// AppAgentByID looks up one registered app agent. Lets the resolver/dashboard
// tell an app-owned agent (and its owning app) apart from orchestrate's own
// seeds and per-user records.
func AppAgentByID(id string) (AppAgentSpec, bool) {
	appAgentsMu.RLock()
	defer appAgentsMu.RUnlock()
	s, ok := appAgentSpecs[id]
	return s, ok
}
