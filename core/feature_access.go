package core

import (
	"sort"
	"strings"
	"sync"
	"github.com/cmcoffee/gohort/core/appagents"
)

// Feature access: the ADMIN gate on outward-facing surfaces a user can expose
// through their own keys. The first is the OpenAI-compatible /v1 endpoint. An
// app DECLARES a shareable feature (RegisterShareableFeature); the admin sets,
// per feature, which users may use it; the surface checks FeatureAllowedForUser
// before doing anything else.
//
// Two independent layers, deliberately with OPPOSITE defaults:
//   - This admin gate is introduced onto a LIVE feature, so absence of a policy
//     means "all users" — turning the gate on doesn't lock out the existing
//     integration. The admin narrows from there. (Mirrors the "empty
//     allowed_users = everyone" convention already used for shared tools and
//     credentials.)
//   - The per-KEY scope (AccountToken.Scope) is deny-by-default: a new key
//     reaches nothing until its owner grants targets.
//
// So the admin says WHETHER a user may use the endpoint at all; the user then
// says which of THEIR keys use it and what each key may reach.

// ShareableFeature is an app-declared surface the admin can gate per user.
type ShareableFeature struct {
	Key   string // stable id, e.g. "openai"; the string checked at the surface
	Label string // admin-facing name
	Desc  string // one line: what granting it lets a user's keys do
}

var (
	shareableFeaturesMu sync.RWMutex
	shareableFeatures   []ShareableFeature
	shareableFeatureKey = map[string]bool{}
)

// RegisterShareableFeature declares a gateable feature. Idempotent by Key; call
// once at init, the same self-registration shape as RegisterApp / route stages.
func RegisterShareableFeature(f ShareableFeature) {
	f.Key = strings.TrimSpace(f.Key)
	if f.Key == "" {
		return
	}
	shareableFeaturesMu.Lock()
	defer shareableFeaturesMu.Unlock()
	if shareableFeatureKey[f.Key] {
		return
	}
	shareableFeatureKey[f.Key] = true
	shareableFeatures = append(shareableFeatures, f)
}

// ShareableFeatures lists declared features in registration order.
func ShareableFeatures() []ShareableFeature {
	foldAppAgentFeatures()
	shareableFeaturesMu.RLock()
	defer shareableFeaturesMu.RUnlock()
	out := make([]ShareableFeature, len(shareableFeatures))
	copy(out, shareableFeatures)
	return out
}

// IsShareableFeature reports whether a key names a registered feature.
func IsShareableFeature(key string) bool {
	foldAppAgentFeatures()
	shareableFeaturesMu.RLock()
	defer shareableFeaturesMu.RUnlock()
	return shareableFeatureKey[strings.TrimSpace(key)]
}

const featureAccessTable = "feature_access"

// FeatureAccessPolicy is the admin's per-feature grant. AllowedUsers empty (or
// no stored record at all) = every user — the non-breaking default. Non-empty =
// only those usernames.
type FeatureAccessPolicy struct {
	Feature      string   `json:"feature"`
	AllowedUsers []string `json:"allowed_users,omitempty"`
}

// LoadFeaturePolicy returns the stored policy for a feature, or a zero policy
// (Feature set, AllowedUsers nil) when none exists — which reads as "all users".
func LoadFeaturePolicy(db Database, feature string) FeatureAccessPolicy {
	p := FeatureAccessPolicy{Feature: strings.TrimSpace(feature)}
	if db != nil && p.Feature != "" {
		db.Get(featureAccessTable, p.Feature, &p)
		p.Feature = strings.TrimSpace(feature) // Get may have overwritten from an older record shape
	}
	return p
}

// SetFeatureAllowedUsers stores the admin grant for a feature. An empty list
// clears the restriction (back to all users). Admin-authorized at the caller.
func SetFeatureAllowedUsers(db Database, feature string, users []string) {
	if db == nil {
		return
	}
	feature = strings.TrimSpace(feature)
	if feature == "" {
		return
	}
	clean := make([]string, 0, len(users))
	seen := map[string]bool{}
	for _, u := range users {
		u = strings.TrimSpace(u)
		if u == "" || seen[strings.ToLower(u)] {
			continue
		}
		seen[strings.ToLower(u)] = true
		clean = append(clean, u)
	}
	sort.Strings(clean)
	db.Set(featureAccessTable, feature, FeatureAccessPolicy{Feature: feature, AllowedUsers: clean})
}

// FeatureAllowedForUser reports whether user may use feature. An unknown feature
// (never registered) is allowed — the gate only constrains features that opted
// in. A feature with no policy, or an empty allow-list, is open to everyone.
func FeatureAllowedForUser(db Database, feature, user string) bool {
	feature = strings.TrimSpace(feature)
	user = strings.TrimSpace(user)
	if feature == "" || user == "" {
		return false
	}
	p := LoadFeaturePolicy(db, feature)
	if len(p.AllowedUsers) == 0 {
		return true // no restriction set — every user (non-breaking default)
	}
	for _, u := range p.AllowedUsers {
		if strings.EqualFold(strings.TrimSpace(u), user) {
			return true
		}
	}
	return false
}

// ExternalTarget is one grantable /v1 target for the per-key scope picker: a
// tier, or an exposed agent/channel the user may reach. Value is exactly what
// AccountToken.AllowsTarget matches ("worker", "agent:<id>", "channel:<chat>").
type ExternalTarget struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Group string `json:"group,omitempty"` // "Tiers" | "Agents" | "Channels" — picker heading
}

// ListExternalTargetsFn is populated by orchestrate at init so the account page
// (which doesn't import orchestrate) can offer a user's grantable targets. Nil
// on a deployment without orchestrate → no agent/channel targets, only tiers.
var ListExternalTargetsFn func(db Database, user string) []ExternalTarget

// ListExternalTargets returns the targets a user may grant to one of their keys:
// the raw tiers plus every exposed agent/channel they own or one is shared to
// them. The candidate set for the scope picker; enforcement matches against the
// key's chosen subset, not this list.
func ListExternalTargets(db Database, user string) []ExternalTarget {
	out := []ExternalTarget{
		{Value: "worker", Label: "worker — fast tier, no agent", Group: "Tiers"},
		{Value: "lead", Label: "lead — strong tier, no agent", Group: "Tiers"},
	}
	if ListExternalTargetsFn != nil {
		out = append(out, ListExternalTargetsFn(db, user)...)
	}
	return out
}

// --- Per-app features (auto-derived from the app-agent registry) --------------
//
// Each app that registers app agents (Servitor, Guides, …) becomes a shareable
// feature "app:<slug>" with zero per-app code: the admin gates WHICH users may
// reach that app's agents from external clients, and the user toggles it per
// API key next to "openai"/"mcp" — the same two-tier control, extended to app
// surfaces. Derived lazily (sync.Once) because app agents register in each
// app's init() and the fold must run after all of them.

// AppFeatureKey maps an app-agent OwningApp label to its feature key
// ("Servitor" → "app:servitor"). Empty label → "".
func AppFeatureKey(owningApp string) string {
	slug := strings.ToLower(strings.TrimSpace(owningApp))
	slug = strings.ReplaceAll(slug, " ", "-")
	if slug == "" {
		return ""
	}
	return "app:" + slug
}

var appFeatureFoldOnce sync.Once

// foldAppAgentFeatures registers one feature per distinct OwningApp. Idempotent
// and lazy — called from every read path that needs the full feature list.
func foldAppAgentFeatures() {
	appFeatureFoldOnce.Do(func() {
		seen := map[string]bool{}
		for _, s := range appagents.AppAgents() {
			k := AppFeatureKey(s.OwningApp)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			RegisterShareableFeature(ShareableFeature{
				Key:   k,
				Label: strings.TrimSpace(s.OwningApp) + " (app agents)",
				Desc:  "Let a user's API keys dispatch " + strings.TrimSpace(s.OwningApp) + "'s agents from external clients (/v1 endpoint, MCP).",
			})
		}
	})
}

// AppFeatureKeyForAgent returns the feature key for an APP-owned agent id, or
// "" for every other agent. The "" return is what keeps the gate a no-op for
// normal user agents.
func AppFeatureKeyForAgent(agentID string) string {
	if s, ok := appagents.AppAgentByID(strings.TrimSpace(agentID)); ok {
		foldAppAgentFeatures()
		return AppFeatureKey(s.OwningApp)
	}
	return ""
}

// KeyAllowsAppAgent is the shared enforcement for dispatching an app-owned
// agent through a key-authenticated EXTERNAL surface (/v1, MCP): admin tier
// (FeatureAllowedForUser, default-open) then key tier (AllowsFeature,
// deny-by-default on scoped keys, nil-scope legacy grandfather). Always ok for
// non-app agents. The denial string is user-actionable and names the app.
func KeyAllowsAppAgent(db Database, user string, token *AccountToken, agentID string) (bool, string) {
	k := AppFeatureKeyForAgent(agentID)
	if k == "" {
		return true, ""
	}
	label := strings.TrimPrefix(k, "app:")
	if s, ok := appagents.AppAgentByID(strings.TrimSpace(agentID)); ok {
		label = s.OwningApp
	}
	if !FeatureAllowedForUser(db, k, user) {
		return false, "an admin has not enabled " + label + " access for your account (Admin > Feature Access)"
	}
	if token != nil && !token.AllowsFeature(k) {
		return false, "this API key is not allowed to reach " + label + " — enable \"" + label + "\" on the key (Account > keys > Configure access)"
	}
	return true, ""
}
