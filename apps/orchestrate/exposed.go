// Helpers for the public /agents/ surface (apps/agents). Orchestrate
// is admin-only; end-users consume agents that an admin has flipped
// Exposed=true on. The agents app calls into orchestrate via the
// exported lookups + handlers below, so the runtime is shared (one
// runner, one session model, one memory store) and only the URL
// surface differs.
//
// Slug rules: <agent name normalized via snakeFromDisplay>. Two
// admins can't both publish "Resume Reviewer" — at exposure time we
// scan for a slug clash and the runtime lookup returns the first
// match in user-listing order. Admins can avoid the clash by
// renaming one of the agents.

package orchestrate

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// DashboardCards implements core.DashboardCardSource — one card per
// exposed agent, served at /agents/<slug>/. The framework calls this
// on every dashboard render so an admin flipping Exposed in the
// editor shows up on the next refresh without restart.
//
// Description is the agent's Description field. Cards land in the
// dashboard's default sort bucket (Order 0 → falls back to 50 in
// the framework, which puts them alongside ordinary apps); admins
// can promote a specific agent by setting a per-agent dashboard
// order later if we add that field.
// ListGrantableApps surfaces every exposed agent as a grantable app
// path for the admin user-apps picker. Returns the FULL list,
// unfiltered by access — the admin needs to see all grantable paths,
// including ones nobody (including themselves as user, separately
// from their admin bit) has been granted.
func (T *OrchestrateApp) ListGrantableApps() []GrantableApp {
	entries := T.ListExposedAgents()
	out := make([]GrantableApp, 0, len(entries))
	for _, e := range entries {
		out = append(out, GrantableApp{
			Path: "/agents/" + e.Slug,
			Name: e.Name + " (agent app)",
		})
	}
	return out
}

func (T *OrchestrateApp) DashboardCards(r *http.Request) []DashboardCard {
	entries := T.ListExposedAgents()
	if len(entries) == 0 {
		return nil
	}
	out := make([]DashboardCard, 0, len(entries))
	for _, e := range entries {
		path := "/agents/" + e.Slug
		// Per-agent access gate — exposed agents are normal apps in
		// the permission system. UserHasAppAccess returns true for
		// admins automatically, and for non-admins when path is in
		// user.Apps or the default-apps list. So an exposed agent
		// that nobody has been granted shows up only for admins,
		// matching the "Publish app, then grant per user" flow.
		if !UserHasAppAccess(r, path) {
			continue
		}
		desc := strings.TrimSpace(e.Description)
		if desc == "" {
			desc = "Chat with " + e.Name + "."
		}
		out = append(out, DashboardCard{
			Name: e.Name,
			Desc: desc,
			Path: path,
		})
	}
	return out
}

// jsonEncode + jsonDecode are tiny adapters so PublicHandleSessionOne
// doesn't repeat the encoding/json boilerplate. Local to this file —
// the orchestrate routes already use encoding/json directly elsewhere.
func jsonEncode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func jsonDecode(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }

// ExposedSlug returns the public URL slug for an agent. Derived
// from PublicName if set (so admins can rebrand the app face
// without renaming the internal agent), else from Name. NOT
// stable across renames — admin renames the slug too, breaking
// bookmarks.
func ExposedSlug(a AgentRecord) string {
	if a.PublicName != "" {
		return SnakeFromDisplay(a.PublicName)
	}
	return SnakeFromDisplay(a.Name)
}

// ExposedDisplayName returns the public-facing name for an agent
// (PublicName if set, else Name). Used for directory cards, the
// chat page title, and the agents-app placeholder copy.
func ExposedDisplayName(a AgentRecord) string {
	if a.PublicName != "" {
		return a.PublicName
	}
	return a.Name
}

// ExposedAgentEntry is one row in the public directory: minimal
// metadata the directory page needs, plus enough hooks to route
// the user to the right chat surface.
type ExposedAgentEntry struct {
	Slug        string
	Name        string
	Description string
	Owner       string
	AgentID     string
}

// ListExposedAgents walks every authenticated user's orchestrate
// sub-store and returns the agents with Exposed=true. Sorted by
// display name for stable ordering.
//
// DB layout note: the USER LIST lives in the root auth-db
// (AuthDB()), but the per-user AGENT sub-stores live under THIS
// app's own bucket (T.DB.Sub("user:<uid>")). Don't conflate the
// two — passing the wrong DB silently returns empty results.
//
// Performance: O(users × agents) per call. Fine at gohort scale
// (<100 users, <20 agents/user); add a deployment-wide index if a
// scan becomes noticeable.
// publiclyExposable reports whether an agent may be served on the public
// /agents/ surface. The ONLY gate is the Exposed flag — Publish means
// published. Earlier this also refused Fleet agents (their delegation /
// standing-agent / event-monitor tools reach owner-only endpoints), but that
// silently dropped the whole publish when you checked the box. The owner-only
// concern is now handled where it belongs: the Fleet toolset is attached only
// when the RUNTIME USER IS THE OWNER (see runner.go's Fleet block), so a public
// visitor never gets those tools even on a Fleet agent — no reason to refuse
// the publish. Cortex agents publish too (each visitor gets their own
// per-(user, agent) home thread).
func publiclyExposable(a AgentRecord) bool {
	// Template seeds are never published directly (Builder clones them), so
	// exclude them regardless of a stale Exposed flag a past save may have set.
	// This is the read-side guard that hides seed-kb from /agents/, the
	// dashboard cards, and the grantable-apps picker without waiting for a
	// re-save to repair the record.
	if isCloneOnlySeed(a.ID) {
		return false
	}
	// Internal app-agents — registered Hidden via RegisterAppAgent (e.g. the
	// Servitor Investigator template, the Guide Author) — are reached only
	// through their owning app, never as a standalone dashboard / /agents/
	// surface. Guard them the same read-side way as clone-only seeds so a stale
	// Exposed flag on a per-user shadow can't leak them onto the dashboard,
	// /agents/, or the grantable-apps picker without a re-save to repair the
	// record. Scoped to app-agents whose registered spec is Hidden, so a user's
	// own published-but-hidden agent (not an app-agent) is unaffected, and an
	// app-agent an app deliberately registers non-Hidden can still be exposed.
	if spec, ok := appagents.AppAgentByID(a.ID); ok && spec.Hidden {
		return false
	}
	return a.Exposed
}

// CortexSessionID exposes a channel agent's pinned home-thread session id so
// the public agents app can pin each visitor to the SAME id the runner
// compacts against (agent.Cortex && sess.ID == cortexSessionID(agent.ID) in
// runner.go). Per-(user, agent) scoping means the id resolves to a different
// physical thread for every visitor without the id itself differing.
func CortexSessionID(agentID string) string { return cortexSessionID(agentID) }

func (T *OrchestrateApp) ListExposedAgents() []ExposedAgentEntry {
	if T.DB == nil || AuthDB == nil {
		return nil
	}
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	// Dedup by AgentID, then by slug, preferring user shadows over
	// in-code seeds. Without the ID-level dedup, a single seed agent
	// surfaces twice when one user has shadowed it (with PublicName,
	// etc.) and another user still sees the default — they produce
	// different slugs but represent the same record.
	byID := map[string]ExposedAgentEntry{}
	idIsShadow := map[string]bool{} // true if the stored entry came from a shadowing user
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(T.DB, u.Username)
		for _, a := range listAgents(udb, u.Username) {
			if !publiclyExposable(a) {
				continue
			}
			slug := ExposedSlug(a)
			if slug == "" {
				continue
			}
			// A user's record is a "shadow" when its Owner matches
			// that user — the in-code seed defaults travel with
			// Owner=seedOwner. Shadow wins over seed.
			isShadow := a.Owner == u.Username
			if prior, exists := byID[a.ID]; exists {
				if idIsShadow[a.ID] || !isShadow {
					_ = prior
					continue
				}
				// Replace seed default with this user's shadow.
			}
			byID[a.ID] = ExposedAgentEntry{
				Slug:        slug,
				Name:        ExposedDisplayName(a),
				Description: a.Description,
				Owner:       u.Username,
				AgentID:     a.ID,
			}
			idIsShadow[a.ID] = isShadow
		}
	}
	// Secondary slug dedup — two distinct agents (different IDs)
	// can still collide on slug (e.g. two admins both publishing
	// "Resume Reviewer"). First match in iteration order wins,
	// matching LookupExposedAgent's semantics.
	out := make([]ExposedAgentEntry, 0, len(byID))
	seenSlug := map[string]bool{}
	for _, e := range byID {
		if seenSlug[e.Slug] {
			continue
		}
		seenSlug[e.Slug] = true
		out = append(out, e)
	}
	return out
}

// LookupAppAgent resolves an agent by ID or name within an owner's store, for
// a data-driven app (customapps) that binds an agent to power its chat
// surface. The name fallback matters for imported apps: a bundle's custom-app
// recipe carries the agent reference normalized to a NAME (an imported agent
// is reborn under a fresh ID), so the binding must dispatch either form —
// same rule as pipeline stage dispatch. Unlike LookupExposedAgent this does
// NOT require Exposed=true — an app's agent is reached through the app, not
// published on its own. Returns (zero, false) when the owner or agent doesn't
// resolve.
func (T *OrchestrateApp) LookupAppAgent(owner, agentID string) (AgentRecord, bool) {
	if T.DB == nil || owner == "" || agentID == "" {
		return AgentRecord{}, false
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return AgentRecord{}, false
	}
	return findAgentByNameOrID(udb, owner, agentID)
}

// LookupExposedAgent resolves a slug to an exposed AgentRecord plus
// the owner's username (used to scope memory/sessions in the right
// sub-store). Returns (zero, "", false) when no exposed agent has
// that slug.
func (T *OrchestrateApp) LookupExposedAgent(slug string) (AgentRecord, string, bool) {
	if T.DB == nil || slug == "" || AuthDB == nil {
		return AgentRecord{}, "", false
	}
	authDB := AuthDB()
	if authDB == nil {
		return AgentRecord{}, "", false
	}
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(T.DB, u.Username)
		for _, a := range listAgents(udb, u.Username) {
			if !publiclyExposable(a) {
				continue
			}
			if ExposedSlug(a) == slug {
				return a, u.Username, true
			}
		}
	}
	return AgentRecord{}, "", false
}

// PublicHandleSend dispatches a /api/send for an exposed agent. The
// caller (apps/agents) has already resolved the slug + checked
// Exposed=true; we just bypass the admin gate that wraps the
// orchestrate-mounted variant and call the runner with the active
// END-USER's identity (not the agent owner's). That way each user's
// sessions/memory/knowledge live in their own per-(user, agent)
// scope — admin builds the agent, end-users accumulate their own
// timeline against it.
func (T *OrchestrateApp) PublicHandleSend(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleSend(w, r, udb, user, agent)
}

// PublicHandleSendWithAppTools is PublicHandleSend plus host-app tools injected
// into the agent's catalog for this run. A data-driven app (customapps) passes a
// co-author tool — a closure over its own record store — so the bound agent can
// write into the open document. The tools are built by the caller (with its own
// data access); orchestrate just runs them, staying ignorant of app storage.
func (T *OrchestrateApp) PublicHandleSendWithAppTools(w http.ResponseWriter, r *http.Request, agent AgentRecord, appTools []AgentToolDef) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleSendWithAppTools(w, r, udb, user, agent, appTools)
}

// PublicHandleCancel mirrors PublicHandleSend's bypass for cancel.
func (T *OrchestrateApp) PublicHandleCancel(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	T.handleCancel(w, r, agent)
}

// PublicHandleRunsActive / PublicHandleRunsDispatch expose the run-stream
// reconnect endpoints on the published /agents/ surface so a long turn whose
// live /api/send socket drops can be resumed from the run buffer — the same
// resilience the admin console has. The underlying handlers already RequireUser
// and refuse a run whose UserID != the caller, so a leaked run id stays private.
func (T *OrchestrateApp) PublicHandleRunsActive(w http.ResponseWriter, r *http.Request) {
	T.handleRunsActive(w, r)
}

func (T *OrchestrateApp) PublicHandleRunsDispatch(w http.ResponseWriter, r *http.Request) {
	T.handleRunsDispatch(w, r)
}

// PublicHandleChannelClear wipes the calling end-user's channel home thread
// (conversation + rolling summary / fold cursor) for an exposed channel agent
// — the per-visitor equivalent of the owner's "Clear channel" console action.
// Per-(user, agent) scoped via RequireUser, so a visitor only ever resets
// their OWN thread. agentID comes from the slug-resolved record, not the
// request, so a crafted ?agent= can't redirect the wipe.
func (T *OrchestrateApp) PublicHandleChannelClear(w http.ResponseWriter, r *http.Request, agentID string) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sid := cortexSessionID(agentID)
	deleteChatSession(udb, agentID, sid) // includes the compact state
	w.WriteHeader(http.StatusNoContent)
}

// (PublicHandleAgentMemory removed — the auto-notes Memory layer
// it served is gone. End-users' per-(user, agent) state now lives
// entirely in Explicit Memory facts and Reference Memory chunks,
// both managed through their respective tools / endpoints.)

// PublicHandlePrivateModeGet / Set expose the per-user Private-
// mode toggle without the admin gate. Reuses the AuthDB-backed
// pref the orchestrate (and legacy chat) surface uses, so a
// user's toggle applies everywhere private-mode lands in send
// body.
func (T *OrchestrateApp) PublicHandlePrivateModeGet(w http.ResponseWriter, r *http.Request) {
	T.handlePrivateModeGet(w, r)
}
func (T *OrchestrateApp) PublicHandlePrivateModeSet(w http.ResponseWriter, r *http.Request) {
	T.handlePrivateModeSet(w, r)
}

// PublicHandleMemoryModeGet / Set expose the per-user Reference Memory
// suppression toggle for the public agents surface. Same per-user
// preference the admin orchestrate surface uses, so toggling either
// flips the bit globally for that user.
func (T *OrchestrateApp) PublicHandleMemoryModeGet(w http.ResponseWriter, r *http.Request) {
	T.handleMemoryModeGet(w, r)
}
func (T *OrchestrateApp) PublicHandleMemoryModeSet(w http.ResponseWriter, r *http.Request) {
	T.handleMemoryModeSet(w, r)
}

// PublicHandleAgentFacts exposes the per-(user, agent) facts list
// (store_fact entries) without the admin gate. Mirrors
// handleAgentFacts for the public surface.
func (T *OrchestrateApp) PublicHandleAgentFacts(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentFacts(w, r, user, agentID)
}

// --- per-SCOPE memory handlers (the agent-as-template display) ----------------
//
// These serve an agent's facts / graph / knowledge / Reference Memory for an
// EXPLICIT scope (e.g. a servitor appliance instance, "app:servitor:<id>") rather
// than the logged-in user. They require a valid session (the auth gate) but read
// the data from scopeUser; the CALLER is responsible for authorizing that the
// session user may view that scope (servitor checks the appliance is theirs
// before calling). This lets servitor mount the SAME editable Memory surface
// orchestrate uses, pointed at a per-appliance scope. agentID is the template's
// id; loadAgent's seed fallback keeps the ownership gate satisfied for it.

func (T *OrchestrateApp) PublicHandleAgentFactsForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentFacts(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraph(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphEntityDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphEntityDelete(w, r, scopeUser, agentID, entityID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphAttrDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphAttrDelete(w, r, scopeUser, agentID, entityID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphAliasDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphAliasDelete(w, r, scopeUser, agentID, entityID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphEdgeDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphEdgeDelete(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentKnowledgeForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentKnowledge(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentInferredListForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentInferredList(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentInferredDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, chunkID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentInferredDelete(w, r, scopeUser, agentID, chunkID)
}

// PublicHandleAgentKnowledgeAutoInferredWipeForScope is the "Wipe all"
// button on the per-scope Memory modal — drops every Reference Memory
// chunk in the scope's namespace. Like the other ForScope handlers, the
// session gate proves a logged-in user; the CALLER authorizes the scope.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeAutoInferredWipeForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentKnowledgeAutoInferredWipe(w, r, scopeUser, agentID)
}

// PublicHandleAgentRecordForScope returns the template agent's record so
// the Memory modal can gate its sections on the disable_explicit /
// disable_inferred flags. The record is config (template-owned), so it
// loads from the scope store via loadAgent's seed fallback — the same
// path the facts handler relies on.
func (T *OrchestrateApp) PublicHandleAgentRecordForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := loadAgent(UserDB(T.DB, scopeUser), agentID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = jsonEncode(w, a)
}

// PublicHandleAgentGraph* expose the per-(user, agent) graph memory (the
// entities + relationships the agent linked about THIS visitor via
// link_entities) on the dashboard surface — read + delete only, scoped via
// RequireUser so a visitor only ever sees and prunes their OWN graph. Same
// data-hygiene posture as the Reference Memory wipe; the underlying handlers are
// already per-user (UserDB + factsNamespace), so these just supply the visitor.
func (T *OrchestrateApp) PublicHandleAgentGraph(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraph(w, r, user, agentID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphEntityDelete(w http.ResponseWriter, r *http.Request, agentID, entityID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphEntityDelete(w, r, user, agentID, entityID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphAttrDelete(w http.ResponseWriter, r *http.Request, agentID, entityID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphAttrDelete(w, r, user, agentID, entityID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphAliasDelete(w http.ResponseWriter, r *http.Request, agentID, entityID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphAliasDelete(w, r, user, agentID, entityID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphEdgeDelete(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphEdgeDelete(w, r, user, agentID)
}

// PublicHandleAgentKnowledge serves the per-(user, agent) vector
// knowledge chunk count + wipe without the admin gate. Mirrors
// handleAgentKnowledge.
func (T *OrchestrateApp) PublicHandleAgentKnowledge(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledge(w, r, user, agentID)
}

// PublicHandleAgentKnowledgeAutoInferredWipe mirrors the admin
// auto-inferred wipe for end-users on the public agent app.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeAutoInferredWipe(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeAutoInferredWipe(w, r, user, agentID)
}

// PublicHandleAgentInferredList exposes the per-(user, agent)
// Reference Memory listing for the public agent app — same payload
// as the admin /inferred endpoint.
func (T *OrchestrateApp) PublicHandleAgentInferredList(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentInferredList(w, r, user, agentID)
}

// PublicHandleAgentInferredDelete deletes one Reference Memory chunk
// for the end-user under their per-(user, agent) namespace.
func (T *OrchestrateApp) PublicHandleAgentInferredDelete(w http.ResponseWriter, r *http.Request, agentID, chunkID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentInferredDelete(w, r, user, agentID, chunkID)
}

// PublicHandleAgentRecord returns a read-only JSON view of the agent
// record so the public chat UI can branch on flag fields
// (disable_explicit, disable_inferred, etc.) without a separate
// per-flag endpoint.
func (T *OrchestrateApp) PublicHandleAgentRecord(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = jsonEncode(w, agent)
}

// PublicHandleAgentKnowledgeUpload mirrors handleAgentKnowledgeUpload
// for the public agent app — same body shape, same per-(user, agent)
// ingest. End-users get to build their own document corpus under the
// exposed agent.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeUpload(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeUpload(w, r, user, agentID)
}

// PublicHandleAgentKnowledgeSources mirrors handleAgentKnowledgeSources.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeSources(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeSources(w, r, user, agentID)
}

// PublicHandleAgentKnowledgeSourceDelete mirrors handleAgentKnowledgeSourceDelete.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeSourceDelete(w http.ResponseWriter, r *http.Request, agentID, reportID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeSourceDelete(w, r, user, agentID, reportID)
}

// PublicHandleSessionList writes the calling user's session
// summaries for the given exposed agent. Resolves the user from
// the request and reads from orchestrate's own DB — apps/agents
// can't pass its per-app bucket here because the sessions are
// stored under orchestrate.
func (T *OrchestrateApp) PublicHandleSessionList(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sessions := listChatSessions(udb, agentID)
	// The Cortex home-thread lives as a session keyed "channel:<agentID>"
	// (see cortexSessionID). Orchestrate's own UI lifts that row out of the
	// list and renders it as the pinned "Cortex" hero (AltNavFlag). The
	// exposed agent-as-an-app front deliberately opts out of that hero
	// (apps/agents/agents.go), so without this filter the same row leaks
	// into the ordinary session rail. Drop it here rather than teaching the
	// shared core/ui runtime about the "channel:" key convention.
	filtered := sessions[:0]
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, "channel:") {
			continue
		}
		filtered = append(filtered, s)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = jsonEncode(w, filtered)
}

// PublicHandleSessionOne is GET (load) / DELETE (drop) / PATCH
// (truncate) for one session under an exposed agent. Method on
// *OrchestrateApp so the udb resolves from orchestrate's T.DB
// (same bucket the sessions were written into by PublicHandleSend).
func (T *OrchestrateApp) PublicHandleSessionOne(w http.ResponseWriter, r *http.Request, agentID, sid string) {
	// Sub-action: /api/sessions/{sid}/export — full session trace
	// download. Same behavior as the admin orchestrate surface; the
	// caller (apps/agents) has already authorized that agent is
	// exposed and the user owns the session.
	if strings.HasSuffix(sid, "/export") {
		sid = strings.TrimSuffix(sid, "/export")
		if sid == "" || strings.Contains(sid, "/") {
			http.NotFound(w, r)
			return
		}
		T.handleSessionExport(w, r, agentID, sid)
		return
	}
	if sid == "" || strings.Contains(sid, "/") {
		http.NotFound(w, r)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s, ok := loadChatSession(udb, agentID, sid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = jsonEncode(w, s)
	case http.MethodDelete:
		deleteChatSession(udb, agentID, sid)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		var body struct {
			At int `json:"at"`
		}
		if err := jsonDecode(r.Body, &body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s, ok := loadChatSession(udb, agentID, sid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if body.At < 0 {
			body.At = 0
		}
		if body.At > len(s.Messages) {
			body.At = len(s.Messages)
		}
		s.Messages = s.Messages[:body.At]
		if _, err := saveChatSession(udb, s); err != nil {
			http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = jsonEncode(w, map[string]any{"at": body.At, "messages_remaining": len(s.Messages)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
