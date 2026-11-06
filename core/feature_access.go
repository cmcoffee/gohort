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
// per feature, which people and teams may use it; the surface checks
// FeatureAllowedForUser before doing anything else.
//
// A list holds people, teams ("group:<id>", every member) and Everyone ("*").
// Once an administrator has saved a list it means exactly what it says, and an
// empty one means nobody (docs/design/external-access.md). Before that:
//   - a feature introduced onto a LIVE surface reads as everyone, so the gate
//     arriving did not lock out an existing integration; the admin narrows
//     from there;
//   - a feature declared Closed (raw compute, say) reads as nobody but the
//     administrators, who always have a Closed feature.
//
// The per-KEY scope (AccountToken.Scope) is a second, independent layer, and
// deny-by-default: a new key reaches nothing until its owner grants targets.
//
// So the admin says WHETHER a user may use the endpoint at all; the user then
// says which of THEIR keys use it and what each key may reach.

// ShareableFeature is an app-declared surface the admin can gate per user.
type ShareableFeature struct {
	Key   string // stable id, e.g. "openai"; the string checked at the surface
	Label string // admin-facing name
	Desc  string // one line: what granting it lets a user's keys do
	// Closed: nobody but the administrators until an administrator adds
	// people or teams, and the administrators always. For a feature whose
	// harm does not wait for a policy, raw compute among them.
	Closed bool
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

// featureClosed reports whether a registered feature is Closed.
func featureClosed(key string) bool {
	shareableFeaturesMu.RLock()
	defer shareableFeaturesMu.RUnlock()
	for _, f := range shareableFeatures {
		if f.Key == strings.TrimSpace(key) {
			return f.Closed
		}
	}
	return false
}

// IsShareableFeature reports whether a key names a registered feature.
func IsShareableFeature(key string) bool {
	foldAppAgentFeatures()
	shareableFeaturesMu.RLock()
	defer shareableFeaturesMu.RUnlock()
	return shareableFeatureKey[strings.TrimSpace(key)]
}

const featureAccessTable = "feature_access"

// ModelAccessFeature is Model access: raw use of the deployment's models with
// no agent in front, the Ollama proxy and the Model API's worker and lead
// tiers (docs/design/external-access.md). Raw compute, so Closed: nobody's
// but the administrators' until granted. Its per-user limits are
// core/modelaccess.
const ModelAccessFeature = "models"

func init() {
	RegisterShareableFeature(ShareableFeature{
		Key:    ModelAccessFeature,
		Label:  "Model access",
		Desc:   "Raw use of the deployment's models with no agent in front: the Ollama proxy, and the Model API's worker and lead tiers. Raw compute, so nobody but administrators until granted.",
		Closed: true,
	})
}

// FeatureEveryone is the list entry for every signed-in user.
const FeatureEveryone = "*"

// FeatureAccessPolicy is the admin's per-feature grant: people, teams
// ("group:<id>") and Everyone ("*"). Saved marks a list an administrator has
// set, which means exactly what it holds, empty being nobody. A list never
// saved this way, a record from before Saved or none at all, keeps its old
// meaning: empty is everyone, unless the feature is Closed.
type FeatureAccessPolicy struct {
	Feature      string   `json:"feature"`
	AllowedUsers []string `json:"allowed_users,omitempty"`
	Saved        bool     `json:"saved,omitempty"`
}

// Closed reports whether the policy's feature is Closed: administrators always
// have it, and nobody else until granted.
func (p FeatureAccessPolicy) Closed() bool { return featureClosed(p.Feature) }

// Everyone reports whether the policy, as it reads now, admits every user.
func (p FeatureAccessPolicy) Everyone() bool {
	if len(p.AllowedUsers) == 0 {
		return !p.Saved && !featureClosed(p.Feature)
	}
	for _, e := range p.AllowedUsers {
		if strings.TrimSpace(e) == FeatureEveryone {
			return true
		}
	}
	return false
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

// SetFeatureAllowedUsers stores the admin grant for a feature: people, teams
// ("group:<id>") and Everyone ("*"). An empty list is nobody. Admin-authorized
// at the caller.
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
	db.Set(featureAccessTable, feature, FeatureAccessPolicy{Feature: feature, AllowedUsers: clean, Saved: true})
}

// FeatureAllowedForUser reports whether user may use feature: by name, through
// a team they are on, or through Everyone; always for an administrator on a
// Closed feature. An unknown feature (never registered) is allowed, since the
// gate only constrains features that opted in. A list an administrator saved
// empty is nobody; one never saved is everyone, or nobody for a Closed one.
func FeatureAllowedForUser(db Database, feature, user string) bool {
	feature = strings.TrimSpace(feature)
	user = strings.TrimSpace(user)
	if feature == "" || user == "" {
		return false
	}
	// A Closed feature is always the administrators'.
	if featureClosed(feature) && UserIsAdmin(user) {
		return true
	}
	p := LoadFeaturePolicy(db, feature)
	if p.Everyone() {
		return true
	}
	for _, e := range p.AllowedUsers {
		e = strings.TrimSpace(e)
		if id, team := strings.CutPrefix(e, shareGroupPrefix); team {
			if id != "" && userInTeam(user, id) {
				return true
			}
			continue
		}
		if strings.EqualFold(e, user) {
			return true
		}
	}
	return false
}

// userInTeam reports whether user is a member of the team (app group) id.
// Any team an administrator made, shareable or not: granting a feature is an
// administrator's act, not a member's share.
func userInTeam(user, id string) bool {
	if AuthDB == nil {
		return false
	}
	adb := AuthDB()
	if adb == nil {
		return false
	}
	if _, ok := LoadAppGroup(adb, id); !ok {
		return false // a removed team grants nothing
	}
	u, ok := AuthGetUser(adb, user)
	if !ok {
		return false
	}
	for _, g := range u.Groups {
		if g == id {
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

// ResolveExternalAgentFn resolves a caller-supplied agent NAME or ID to the
// canonical agent id, only when that agent is reachable from external
// key-authenticated surfaces (MCPExposed user agents, or app agents whose app
// feature the admin enabled). The MCP server uses it so its per-app key gate
// sees the real id, not whatever string the client typed. Nil ⇒ orchestrate
// not loaded; callers fall back to their legacy gate.
// The granted callback (nil ok) reports whether the CALLING KEY explicitly
// names a canonical target — an explicit grant is consent enough to reach an
// agent that isn't otherwise exposed.
var ResolveExternalAgentFn func(db Database, owner, key string, granted func(canonical string) bool) (string, bool)

// ExternalAgentInfo is one agent an external client may drive, in the form that
// client needs to choose between them: the id it must pass back, the name a
// person recognizes, and what the agent is FOR.
type ExternalAgentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// ParentID names the agent that owns this one, when it is a sub-agent.
	// Marked rather than hidden: the resolver dispatches to any exposed agent,
	// so omitting sub-agents would make the list narrower than what a call
	// accepts — and would silently override an owner who ticked the toggle.
	ParentID string `json:"parent_id,omitempty"`
}

// ListExternalReachableAgentsFn lists the agents an external key-authenticated
// caller may dispatch to.
//
// The db argument is IGNORED by the registered implementation, which binds the
// store agents actually live in. It stays in the signature because a caller
// legitimately holds its own store and nothing at the call site would tell it
// that store is the wrong one here — passing it and having it ignored is safer
// than an API that looks like it needs one. It exists so a client can DISCOVER what to pass to
// ask_agent — the schema asks for an agent id and, without this, nothing tells
// the caller which ids exist, so it guesses or takes the default.
//
// It must return exactly what ResolveExternalAgentFn would ACCEPT for the same
// caller — no more, and no less. More would enumerate agents the owner kept off
// this surface, turning a convenience into a disclosure; less would show a
// caller a set it cannot act on, which is the unanchored-list problem the other
// way round. The granted callback is the same key-level consent the resolver
// takes. Nil ⇒ orchestrate not loaded; the caller has nothing to list.
var ListExternalReachableAgentsFn func(db Database, owner string, granted func(canonical string) bool) []ExternalAgentInfo

// ListExternalTargets returns the targets a user may grant to one of their keys:
// the raw tiers plus every exposed agent/channel they own or one is shared to
// them. The candidate set for the scope picker; enforcement matches against the
// key's chosen subset, not this list.
func ListExternalTargets(db Database, user string) []ExternalTarget {
	var out []ExternalTarget
	// The raw tiers are Model access, which an administrator grants.
	if FeatureAllowedForUser(RootDB, ModelAccessFeature, user) {
		out = append(out,
			ExternalTarget{Value: "worker", Label: "worker: fast tier, no agent", Group: "Tiers"},
			ExternalTarget{Value: "lead", Label: "lead: strong tier, no agent", Group: "Tiers"})
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

// appFeatureKey maps an app-agent OwningApp label to its feature key
// ("Servitor" → "app:servitor"). Empty label → "".
func appFeatureKey(owningApp string) string {
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
			k := appFeatureKey(s.OwningApp)
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
		return appFeatureKey(s.OwningApp)
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
		return false, "this API key is not allowed to reach " + label + ", enable \"" + label + "\" on the key (Account, API keys, Access, under Can reach)"
	}
	return true, ""
}
