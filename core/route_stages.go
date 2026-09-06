package core

import (
	"strings"
	"sync"
)

// RouteStage represents a lead-LLM call site that can be optionally
// downgraded to the worker LLM via the routing menu. Apps self-register
// their stages via RegisterRouteStage in init().
type RouteStage struct {
	Key           string // db key, e.g. "myapp.stage_name"
	Label         string // menu label
	Default       string // effective value when not set in DB: "lead", "lead (thinking)", "worker", or "worker (thinking)"
	DefaultBudget int    // default thinking budget tokens for this stage; 0 means fall back to global
	Group         string // display group in the admin routing UI; derived from key prefix if empty
	Private       bool   // when true, the stage is locked to worker tier (private-only app)
	// App names the app this call site belongs to, as its WebPath
	// ("/techwriter"). Empty means framework-level routing that belongs to no
	// single app, which is what every existing registration means today.
	//
	// DECLARED, and never inferred from Key. The key prefixes look like a
	// convention and are two conventions plus exceptions — app.techwriter
	// beside blogger.editor beside admin.tool_groups.suggest — and a wrong
	// guess here does not read as a guess to whoever finds it: it reads as a
	// dial that is theirs to move.
	App string
}

var routeRegistry struct {
	mu     sync.RWMutex
	stages []RouteStage
	byKey  map[string]bool
}

// RegisterRouteStage registers a routable lead-LLM call site so it appears
// in the routing menu. Default routing is lead; users can opt into worker.
func RegisterRouteStage(s RouteStage) {
	routeRegistry.mu.Lock()
	defer routeRegistry.mu.Unlock()
	if routeRegistry.byKey == nil {
		routeRegistry.byKey = make(map[string]bool)
	}
	if routeRegistry.byKey[s.Key] {
		return
	}
	routeRegistry.byKey[s.Key] = true
	routeRegistry.stages = append(routeRegistry.stages, s)
}

// ListRouteStages returns all registered route stages in registration order.
func ListRouteStages() []RouteStage {
	routeRegistry.mu.RLock()
	defer routeRegistry.mu.RUnlock()
	out := make([]RouteStage, len(routeRegistry.stages))
	copy(out, routeRegistry.stages)
	return out
}

// RouteStagesForApp returns the stages an app has CLAIMED, in registration
// order. Empty for an app that has claimed none, which is every app until it
// declares them — a control with no declared owner stays on its mechanism tab
// and appears under nobody.
func RouteStagesForApp(appPath string) []RouteStage {
	if appPath == "" {
		return nil
	}
	routeRegistry.mu.RLock()
	defer routeRegistry.mu.RUnlock()
	var out []RouteStage
	for _, s := range routeRegistry.stages {
		if s.App == appPath {
			out = append(out, s)
		}
	}
	return out
}

// IsPrivateStage reports whether the given stage key is REGISTERED as private.
//
// Registration is a property of the app and never changes at runtime. Whether
// the pin is currently ENFORCED is a separate question — see RouteToLead, which
// lifts it when every configured model is private — so callers deciding what a
// user may choose should ask PrivateStageEnforced instead.
func IsPrivateStage(key string) bool {
	routeRegistry.mu.RLock()
	defer routeRegistry.mu.RUnlock()
	for _, s := range routeRegistry.stages {
		if s.Key == key && s.Private {
			return true
		}
	}
	return false
}

// PrivateStageEnforced reports whether a stage's private pin is currently
// binding: registered private AND not lifted by an all-private deployment.
//
// Separate from IsPrivateStage so a UI can offer the lead tier exactly when the
// runtime would honor it. The admin write path previously refused a lead value
// on any private stage; refusing one the runtime would now accept is a setting
// that reads as broken.
func PrivateStageEnforced(key string) bool {
	return IsPrivateStage(key) && !AllLLMsPrivate()
}

// LookupRouteFunc is set by the application to read a route stage's
// current setting from the database. Returns "worker" or "" (lead).
var LookupRouteFunc func(key string) string

// LookupRouteThinkBudgetFunc is set by the application to read per-route
// thinking budget overrides. Returns &N or nil (use global default).
var LookupRouteThinkBudgetFunc func(key string) *int

// routeEffectiveVal returns the effective routing value for key,
// falling back to the stage's Default when the DB has no stored value.
// RouteOverride returns the route value an operator has STORED for a key, and
// nothing else: no registry default, no fallback.
//
// It exists because routeEffectiveVal answers a different question than some
// callers are asking. Its fallback chain ends at the empty string, and
// RouteValueIsLead treats the empty string as lead — correct for a compiled
// call site, which is registered at startup and whose author decided lead was
// the right default. It is wrong for a call site that already HAS a default of
// its own and only wants to know whether somebody overrode it. Asking
// RouteToLead about a key nobody registered gets "lead" for an answer, which
// is how an unregistered key silently becomes the expensive one.
//
// Empty means nobody has set it. That is the whole point.
func RouteOverride(key string) string {
	if key == "" || LookupRouteFunc == nil {
		return ""
	}
	return strings.TrimSpace(LookupRouteFunc(key))
}

func routeEffectiveVal(key string) string {
	val := ""
	if LookupRouteFunc != nil {
		val = LookupRouteFunc(key)
	}
	if val == "" {
		routeRegistry.mu.RLock()
		for _, s := range routeRegistry.stages {
			if s.Key == key {
				val = s.Default
				break
			}
		}
		routeRegistry.mu.RUnlock()
	}
	return val
}

// RouteToLead returns true if the named route stage should use the lead
// LLM. Stages default to lead unless their Default field or DB value says
// "worker" or "worker (thinking)". Stages registered with Private=true
// are locked to the worker tier and never escalate, regardless of DB
// value — used by apps (e.g. servitor) that handle sensitive data and
// must not send it to a remote lead model.
func RouteToLead(key string) bool {
	// A private stage is pinned because escalating would send sensitive
	// material to a REMOTE lead. When the operator has declared that no model
	// this deployment uses leaves their control, that premise is false and the
	// pin is pure cost: the app holding the most sensitive data becomes the
	// only one permanently denied the better reasoner.
	//
	// The declaration is explicit (see AllLLMsPrivate) rather than inferred,
	// because inferring it wrongly sends SSH credentials to a hosted provider
	// and there is no un-sending them.
	if IsPrivateStage(key) && !AllLLMsPrivate() {
		return false
	}
	return RouteValueIsLead(routeEffectiveVal(key))
}

// RouteValueIsLead reports whether a raw route VALUE escalates to the lead
// tier. Worker values are the closed set; everything else (including the empty
// string, which means "stage default") escalates.
//
// Exported so the admin write path tests the same thing the runtime does. It
// previously kept its own copy of the rule as a literal `value == "lead"`,
// which silently stopped covering the tier the moment a second lead value
// existed — letting a Private stage store one. Runtime still refuses (the
// IsPrivateStage check above), but a stored value that the runtime overrides
// is exactly the kind of setting that reads as broken.
func RouteValueIsLead(val string) bool {
	return val != "worker" && val != "worker (thinking)"
}

// RouteValues are the four legal route values: the two tiers × explicit
// thinking. Tier and thinking are independent — see RouteThink.
func RouteValues() []string {
	return []string{"lead", "lead (thinking)", "worker", "worker (thinking)"}
}

// RouteThink returns the thinking override for the named route stage.
//   - "worker (thinking)" → &true
//   - "lead (thinking)"   → &true
//   - "worker"            → &false
//   - "lead" / ""         → nil (provider default)
//
// Thinking used to be expressible on WORKER routes only, which coupled two
// decisions to one control: escalating a stage to the lead also dropped the
// explicit thinking flag it had as a worker, because "lead" fell through to
// nil here. So the moment an agent moved to the stronger model, the framework
// stopped asking it to reason harder — backwards, and invisible, since the
// admin table showed only the tier. Tier and thinking are now independent:
// each is spelled out in the value.
func RouteThink(key string) *bool {
	val := routeEffectiveVal(key)
	switch val {
	case "worker (thinking)", "lead (thinking)":
		t := true
		return &t
	case "worker":
		f := false
		return &f
	}
	return nil
}

// RouteThinkBudget returns the per-route thinking token budget, checking
// (in order): DB override → stage DefaultBudget → nil (use global default).
func RouteThinkBudget(key string) *int {
	if LookupRouteThinkBudgetFunc != nil {
		if n := LookupRouteThinkBudgetFunc(key); n != nil {
			return n
		}
	}
	routeRegistry.mu.RLock()
	defer routeRegistry.mu.RUnlock()
	for _, s := range routeRegistry.stages {
		if s.Key == key && s.DefaultBudget > 0 {
			n := s.DefaultBudget
			return &n
		}
	}
	return nil
}
