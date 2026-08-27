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
	// ForcePrivate declares that this agent handles material which must not
	// reach a third-party model — SSH credentials, log contents, system facts.
	// It locks the agent out of the lead tier the same way RouteStage.Private
	// locks a pipeline stage out of it, and lifts under the same condition: an
	// operator declaring that every configured model is private (see
	// core.AllLLMsPrivate).
	//
	// DECLARED, not defaulted. Servitor's investigator stayed off the lead tier
	// for a while only because LeadModel happens to be false in a zero
	// AgentRecord — a property nothing enforced and one edit would have undone.
	// An agent that must not escalate has to SAY so.
	ForcePrivate bool `json:"force_private,omitempty"`
	// DispatchMode is how far this agent may dispatch: which OTHER agents,
	// pipelines and machines its `agents` tool can reach.
	//
	// BLANK MEANS NONE, and that inversion is deliberate. Everywhere else a
	// blank dispatch mode resolves to "all" — every non-hidden agent in the
	// user's fleet — which is the right default for an agent someone built and
	// put in their fleet on purpose. It is the wrong one here. An app agent is
	// hidden, bound to its app's surface, and reached through that app rather
	// than chosen from a picker; and the `agents` tool is a FRAMEWORK tool
	// appended to every turn regardless of AllowedTools, so leaving it off a
	// spec's allowlist withholds nothing. Left to the ordinary default, every
	// app agent ever registered could dispatch to the user's whole fleet
	// without its author choosing that or being able to see it. Guides' Guide
	// Author was doing exactly that, to agents its guide had never attached.
	//
	// So reaching the fleet is opt-IN here: set DispatchAll to say an app agent
	// really is meant to delegate. DispatchOnly / DispatchExcept are accepted
	// but need a target list, which a static spec cannot name (agent ids are
	// per-user and made at runtime) — an app that wants a scoped list sets the
	// mode and the targets on its per-turn record copy instead, the way guides
	// binds dispatch to the open guide's attached agent Sources.
	DispatchMode string `json:"dispatch_mode,omitempty"`
}

// Dispatch modes for AppAgentSpec.DispatchMode. Spelled here so a spec need not
// import orchestrate (this package is a stdlib-only leaf, on purpose) and so the
// two cannot drift: orchestrate's exported names alias the same strings.
const (
	DispatchAll    = "all"    // any non-hidden agent — opt in deliberately
	DispatchOnly   = "only"   // allowlist; needs targets set at runtime
	DispatchExcept = "except" // denylist; needs targets set at runtime
	DispatchNone   = "none"   // no dispatch at all (the default for an app agent)
)

// EffectiveDispatchMode resolves a spec's declared policy, applying the
// blank-means-none default above. Always ask through this rather than reading
// the field, so the default lives in one place.
func (s AppAgentSpec) EffectiveDispatchMode() string {
	switch s.DispatchMode {
	case DispatchAll, DispatchOnly, DispatchExcept, DispatchNone:
		return s.DispatchMode
	default:
		return DispatchNone
	}
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
