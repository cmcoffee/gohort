// Admin re-scoping of runtime tools — the write side of "change a tool's
// scope after the fact." orchestrate owns agent storage (AgentRecord), so
// the AgentRecord mutation lives here; the admin app calls in through the
// core callback vars (core.AdminToolScopeState / core.AdminSetToolScope /
// core.AdminRehomeOrphanTool) and never imports orchestrate or its types.
// Mirrors the OnTempToolApproved decoupling pattern.
//
// The scope pill control drives everything: a Global pill plus one pill per
// agent, toggled through AdminSetToolScope (promote, descope, per-agent
// enable/disable). Orphan re-homing and the scope-state read live here too.
package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// handleToolScope serves the in-chat pill control (Tools modal). GET returns
// the scope state for a tool; POST applies one toggle. Owner is always the
// requesting user — they manage their own agents' tool scopes. Passes RootDB
// (the process root) so the same agentUserDB derivation the admin path uses
// resolves to this user's orchestrate store.
func (T *OrchestrateApp) handleToolScope(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	// kind selects the scope backend (tool | pipeline | credential); the
	// in-chat control historically only manages tools, so default there.
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "tool"
	}
	prov, ok := ScopeProviderFor(kind)
	if !ok {
		http.Error(w, "unknown scope kind: "+kind, http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		st, found := prov.State(RootDB, user, name)
		if !found {
			http.Error(w, kind+" not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// No caching: the pill modal re-reads this immediately after each
		// toggle POST, and a stale cached body snaps the pill back on.
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(st)
	case http.MethodPost:
		var body struct {
			Target string `json:"target"`
			On     bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body.Target = strings.TrimSpace(body.Target)
		if body.Target == "" {
			http.Error(w, "target required", http.StatusBadRequest)
			return
		}
		if err := prov.Set(RootDB, user, name, body.Target, body.On); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func init() {
	AdminToolScopeState = toolScopeState
	AdminSetToolScope = setToolScope
	AdminRehomeOrphanTool = rehomeOrphanTool
	// Kind-aware scope registry — the same pill UI + HTTP handlers drive
	// every kind. "tool" is the original; pipeline/credential register in
	// their own files (admin_pipeline_scope.go / admin_credential_scope.go).
	RegisterScopeProvider("tool", ScopeProvider{State: toolScopeState, Set: setToolScope})
}

// missingDeps returns dependency descriptors that don't resolve, for a
// row badge. Today: an api/toolbox tool whose credential isn't registered.
func missingDeps(t TempTool) []string {
	var miss []string
	cred := strings.TrimSpace(t.Credential)
	if cred != "" && !strings.EqualFold(cred, "no_auth") {
		if exists, _, _ := Secure().CredentialStatus(cred); !exists {
			miss = append(miss, "credential:"+cred)
		}
	}
	return miss
}

// agentSeesGlobalTool reports whether a GLOBAL tool is currently on for an
// agent: not in its deny-list, and (if it carries a user-crafted allow-list)
// present in it. A default agent (nil/sentinel allow-list) sees everything.
func agentSeesGlobalTool(a AgentRecord, name string) bool {
	for _, d := range a.DisabledPersistentTools {
		if d == name {
			return false
		}
	}
	// The no-tools sentinel means exactly that — the agent sees NO tools, so it
	// cannot see a global one either. This must be tested BEFORE the allow-list
	// branch: the sentinel is not a real tool name, so a combined
	// `len>0 && !isNoToolsSentinel` guard skips the branch entirely and falls
	// through to the default-pool `return true`, reporting a zero-tool agent as
	// seeing EVERY global tool.
	if isNoToolsSentinel(a.AllowedTools) {
		return false
	}
	if len(a.AllowedTools) > 0 {
		canon := canonicalToolName(name)
		for _, al := range a.AllowedTools {
			if canonicalToolName(al) == canon {
				return true
			}
		}
		return false
	}
	return true
}

// toolScopeState builds the pill picture for a tool across the owner's agents.
func toolScopeState(db Database, owner, toolName string) (ToolScopeState, bool) {
	st := ToolScopeState{Name: toolName, Agents: []ToolScopeAgent{}}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return st, false
	}
	// Flattened namespace: ONE record answers everything. Empty ScopeAgents =
	// the Global pill; a non-empty list = exactly the agents carrying it.
	row, rowFound := UserToolByName(db, owner, toolName)
	st.Global = rowFound && len(row.ScopeAgents) == 0

	agents := listAgents(udb, owner)
	// scopeTarget: only top-level, user-managed agents are tool-scope targets.
	// Exclusions are by IDENTITY, never by the Hidden flag:
	//   - app-specific agents (Guide Author, Servitor Investigator, …) carry an
	//     app-declared kit, so they're never a tool-scope target;
	//   - clone-only seeds are templates you clone, not agents you run;
	//   - sub-agents (OwnedBy) are managed through their parent.
	//
	// Hidden was previously part of this test and that was wrong in both
	// directions: it LEAKED for a visible app agent (Casefile's "Case Analyzer"),
	// and it SUPPRESSED a user's own hidden agent — which is a real agent that
	// can hold tools. Hidden means "keep this out of the fleet/dispatch picker",
	// not "this agent may not have tools", so a user hiding an agent should not
	// make it impossible to give that agent a tool.
	//
	// Sub-agents (OwnedBy set) ARE targets. They run their own turns off their
	// own Tools, and picking up the parent's kit is opt-in per sub-agent
	// (InheritParentTools), so "scoped to the parent" does not imply the
	// specialist can call it. Leaving them out made a whole tier of agents
	// unreachable from the picker. They carry ParentID so the UI can nest them
	// under their parent rather than listing a specialist as a peer.
	// agentHas is built FIRST, over every non-app agent (seeds included), so
	// the pill filter below can keep a seed listed when it already holds the
	// tool — an existing grant must stay visible and revocable even though
	// seeds are no longer OFFERED as targets.
	agentHas := map[string]bool{}
	if rowFound {
		for _, id := range row.ScopeAgents {
			agentHas[id] = true
		}
	}
	scopeTarget := func(a AgentRecord) bool {
		if isAppAgent(a.ID) || isCloneOnlySeed(a.ID) {
			return false
		}
		// Framework seeds (Builder, the retired Chat seed, Research, …) are
		// not offered as per-agent targets for USER tools: Builder always
		// loads the full pool (its pill would be a no-op), and the seed
		// personas are retired in favour of archetype recipes. Matched BY
		// IDENTITY, not ownership: a seed the user has customized exists as a
		// SHADOW — the seed's ID with the user's Owner — and the old
		// Owner==seedOwner test let every shadow through, which is exactly how
		// "Chat" kept turning up in the pills after its retirement. Exception:
		// a seed that already HOLDS this tool stays listed (see agentHas), so
		// an existing grant is always revocable.
		if isSeedID(a.ID) || a.Owner == seedOwner {
			return agentHas[a.ID]
		}
		return true
	}
	if rowFound {
		st.Missing = missingDeps(row.Tool)
	}
	// Emit parents first, then each parent's children directly beneath it, so a
	// consumer that just walks the slice renders the tree in order. Sub-agents
	// whose parent is not itself a target (an app agent's helper, say) are
	// emitted at top level rather than dropped.
	emit := func(a AgentRecord, parent string) {
		on := agentHas[a.ID]
		if st.Global {
			on = agentSeesGlobalTool(a, toolName)
		}
		st.Agents = append(st.Agents, ToolScopeAgent{
			ID: a.ID, Name: a.Name, On: on, ParentID: parent,
		})
	}
	isTarget := map[string]bool{}
	for i := range agents {
		if scopeTarget(agents[i]) {
			isTarget[agents[i].ID] = true
		}
	}
	for i := range agents {
		if !scopeTarget(agents[i]) || (agents[i].OwnedBy != "" && isTarget[agents[i].OwnedBy]) {
			continue
		}
		emit(agents[i], "")
		for j := range agents {
			if scopeTarget(agents[j]) && agents[j].OwnedBy == agents[i].ID {
				emit(agents[j], agents[i].ID)
			}
		}
	}
	// The agent list is filled in even when the tool is in NO scope (found=false):
	// an orphan or an unkept draft still needs somewhere to be re-homed, and the
	// caller can only offer that choice if it gets the targets. Every pill is
	// simply off.
	if !st.Global && len(agentHas) == 0 {
		return st, false
	}
	return st, true
}

// setToolScope applies one pill toggle. See core.AdminSetToolScope doc.
func setToolScope(db Database, owner, toolName, target string, on bool) error {
	st, ok := toolScopeState(db, owner, toolName)
	if !ok {
		return fmt.Errorf("tool %q not found in any scope", toolName)
	}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return fmt.Errorf("no agent store for user %q", owner)
	}
	if target == "global" {
		if on {
			if st.Global {
				return nil
			}
			return promoteScopedToGlobal(db, udb, owner, toolName)
		}
		if !st.Global {
			return nil
		}
		return demoteGlobalToScoped(db, udb, owner, toolName, st)
	}
	// Per-agent toggle.
	if st.Global {
		if on {
			return attachGlobalToolToAgent(db, owner, target, toolName)
		}
		return disableGlobalToolForAgent(udb, owner, target, toolName)
	}
	// Agent-scoped: add/remove a copy on the target agent.
	if on {
		def := scopedDefOf(udb, owner, toolName)
		if def == nil {
			return fmt.Errorf("no definition available for %q", toolName)
		}
		return bundleAgentToolByID(udb, owner, target, *def)
	}
	// OFF at agent scope: drop this agent from the record's ScopeAgents.
	// unbundleAgentToolByID orphans the record when this was the LAST
	// carrier — same survival guarantee agent-delete gives — so no
	// snapshot/re-check dance is needed here anymore.
	return unbundleAgentToolByID(udb, owner, target, toolName)
}

// promoteScopedToGlobal lifts an agent-scoped tool into the pool and strips
// every agent copy — the Global-pill ON transition from agent scope.
func promoteScopedToGlobal(db, udb Database, owner, toolName string) error {
	p, ok := UserToolByName(db, owner, toolName)
	if !ok || len(p.ScopeAgents) == 0 {
		return fmt.Errorf("no agent-scoped %q to promote", toolName)
	}
	// Flattened namespace: promote = clear the scope restriction. One record,
	// nothing to copy, nothing to strip — the "promoted but it came back on
	// Case Analyzer" class of bug is structurally impossible now.
	if !SetUserToolScopeAgents(db, owner, toolName, nil) {
		return fmt.Errorf("promote %q: scope update failed", toolName)
	}
	return nil
}

// demoteGlobalToScoped removes a tool from the pool and bundles a copy onto
// every agent that currently sees it (Global-pill OFF = descope to the ON
// agents). Disabled/denied agents are left without a copy.
func demoteGlobalToScoped(db, udb Database, owner, toolName string, st ToolScopeState) error {
	p, ok := UserToolByName(db, owner, toolName)
	if !ok || len(p.ScopeAgents) != 0 {
		return fmt.Errorf("%q is not a global tool", toolName)
	}
	// Flattened namespace: demote = restrict the ONE record to the currently-ON
	// agents. No copies land anywhere.
	var onAgents []string
	for _, ag := range st.Agents {
		if ag.On {
			onAgents = append(onAgents, ag.ID)
		}
	}
	// Global OFF with no agent to descope onto would otherwise hard-delete the
	// only copy. Orphan it instead so it stays re-homeable — same guarantee the
	// agent-scope OFF path gives.
	if len(onAgents) == 0 {
		AddOrphanedTempTools(db, owner, []OrphanedTempTool{{
			Tool:       p.Tool,
			OrphanedAt: time.Now(),
		}})
		Log("[temptool.scope] orphaned %q on global-OFF with no descope target", toolName)
		return DeletePersistentTempTool(db, owner, toolName)
	}
	if !SetUserToolScopeAgents(db, owner, toolName, onAgents) {
		return fmt.Errorf("demote %q: scope update failed", toolName)
	}
	return nil
}

// disableGlobalToolForAgent hides a global tool from one agent: drop it from
// a user-crafted allow-list if that's how the agent gates, else add it to the
// deny-list.
func disableGlobalToolForAgent(udb Database, owner, agentID, toolName string) error {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return fmt.Errorf("agent %q not found", agentID)
	}
	if len(rec.AllowedTools) > 0 && !isNoToolsSentinel(rec.AllowedTools) {
		canon := canonicalToolName(toolName)
		kept := rec.AllowedTools[:0]
		for _, al := range rec.AllowedTools {
			if canonicalToolName(al) != canon {
				kept = append(kept, al)
			}
		}
		// Removing the LAST entry must not empty the list: an empty AllowedTools
		// reads as "sees the whole default pool" everywhere (agentSeesGlobalTool
		// above, agents.go:540), so an agent pinned to exactly one tool would
		// flip to EVERY tool on turning that tool's pill off — the precise
		// opposite of the intent. Write the explicit no-tools sentinel instead.
		if len(kept) == 0 {
			rec.AllowedTools = []string{noToolsSentinel}
		} else {
			rec.AllowedTools = kept
		}
		_, err := saveAgent(udb, rec)
		return err
	}
	for _, d := range rec.DisabledPersistentTools {
		if d == toolName {
			return nil // already denied
		}
	}
	rec.DisabledPersistentTools = append(rec.DisabledPersistentTools, toolName)
	_, err := saveAgent(udb, rec)
	return err
}

// scopedDefOf returns the definition of an agent-scoped tool from the
// unified store (a record whose ScopeAgents restricts it to specific agents).
func scopedDefOf(udb Database, owner, toolName string) *TempTool {
	if p, ok := UserToolByName(udb, owner, toolName); ok && len(p.ScopeAgents) > 0 {
		t := p.Tool
		return &t
	}
	return nil
}

// bundleAgentToolByID loads an agent by id and bundles (replace-by-name) a
// tool onto its record.
func bundleAgentToolByID(udb Database, owner, agentID string, t TempTool) error {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return fmt.Errorf("agent %q not found", agentID)
	}
	// App agents are not tool-scope targets — their kit is app-declared. The
	// scope pill already excludes them (scopeTarget), but guard the write path
	// too so no caller can bundle onto one. Removal stays open (unbundle is
	// unguarded) so an already-mis-scoped tool can still be cleaned off.
	if isAppAgent(rec.ID) {
		return fmt.Errorf("cannot scope a tool onto app agent %q — app agents get their tools from the owning app, not the LLM-authored plane", rec.Name)
	}
	// No Owner-field equality guard: the agent was loaded from the resolved user
	// store (agentUserDB) and this is the admin-driven scope path, which scopes a
	// tool onto ANY of the owner's agents — including SEED agents like Builder
	// (Owner==seedOwner) and sub-agents whose .Owner differs. The equality check
	// wrongly rejected exactly those with "not your agent" (mirrors the
	// credential/pipeline scope fix).
	//
	// Flattened namespace: the definition lives in the user's unified store —
	// "bundling" = upsert the ONE record and make sure this agent is in its
	// scope. A shared record (empty ScopeAgents) is already visible to the
	// agent; only its definition updates. AgentRecord.Tools is no longer
	// written.
	existing, had := UserToolByName(udb, owner, t.Name)
	if err := AdminPersistTempTool(udb, owner, t); err != nil {
		return err
	}
	if had && len(existing.ScopeAgents) == 0 {
		return nil // shared — def updated in place, visibility unchanged
	}
	if existing.ScopedToAgent(rec.ID) {
		return nil // def updated; scope already includes this agent
	}
	scope := append(append([]string{}, existing.ScopeAgents...), rec.ID)
	if !SetUserToolScopeAgents(udb, owner, t.Name, scope) {
		return fmt.Errorf("bundle %q: scope update failed", t.Name)
	}
	return nil
}

// unbundleAgentToolByID removes a tool from an agent's record — the OFF twin of
// bundleAgentToolByID, and like it it carries NO Owner-field equality guard.
// The admin scope path removes a tool from ANY of the owner's agents, including
// SEED/app agents (Owner==seedOwner, e.g. Casefile's "Case Analyzer") and
// sub-agents whose .Owner differs. runner.go's unbundleAgentTool keeps that
// guard for the RUNTIME sess.UnbundleTool caller, but here it only mis-fired:
// it made the Access selector's unselect return "not your agent", and it made
// promoteScopedToGlobal's strip silently fail on those agents — so a tool
// promoted to global kept its scoped copy and "came back" after the global one
// was deleted. Loaded from the resolved user store; saving writes the shadow.
func unbundleAgentToolByID(udb Database, owner, agentID, toolName string) error {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return fmt.Errorf("agent %q not found", agentID)
	}
	// Flattened namespace: drop this agent from the record's ScopeAgents.
	// The LAST carrier orphans the record instead of deleting it (or silently
	// promoting it to shared) — the same survival guarantee the old
	// last-scope-pill-OFF path implemented at its call sites.
	p, found := UserToolByName(udb, owner, toolName)
	if !found || !p.ScopedToAgent(agentID) {
		return fmt.Errorf("tool %q is not bundled on agent %q", toolName, rec.Name)
	}
	kept := make([]string, 0, len(p.ScopeAgents))
	for _, id := range p.ScopeAgents {
		if id != agentID {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		AddOrphanedTempTools(udb, owner, []OrphanedTempTool{{
			Tool:            p.Tool,
			FormerAgentID:   agentID,
			FormerAgentName: rec.Name,
			OrphanedAt:      time.Now(),
		}})
		Log("[temptool.scope] orphaned %q after removing its last carrier %q", toolName, rec.Name)
		return DeletePersistentTempTool(udb, owner, toolName)
	}
	if !SetUserToolScopeAgents(udb, owner, toolName, kept) {
		return fmt.Errorf("unbundle %q: scope update failed", toolName)
	}
	return nil
}

// rehomeOrphanTool moves an orphaned tool to global or onto an agent, then
// clears it from the orphan store.
func rehomeOrphanTool(db Database, owner, toolName, target string) error {
	var def *TempTool
	for _, o := range LoadOrphanedTempTools(db, owner) {
		if o.Tool.Name == toolName {
			t := o.Tool
			def = &t
			break
		}
	}
	if def == nil {
		return fmt.Errorf("no orphaned tool named %q", toolName)
	}
	if target == "global" {
		if err := AdminPersistTempTool(db, owner, *def); err != nil {
			return err
		}
	} else {
		udb := agentUserDB(db, owner)
		if udb == nil {
			return fmt.Errorf("no agent store for user %q", owner)
		}
		if err := bundleAgentToolByID(udb, owner, target, *def); err != nil {
			return err
		}
	}
	RemoveOrphanedTempTool(db, owner, toolName)
	return nil
}

// captureOrphanedTools moves an about-to-be-deleted agent's agent-scoped
// tools into the owner's orphan store, so they survive the delete for the
// admin to re-home. Skips any name that also lives in the global pool (the
// global copy remains, so it isn't orphaned). Best-effort; never blocks the
// delete.
func captureOrphanedTools(db Database, owner string, agent AgentRecord) {
	if db == nil || owner == "" {
		return
	}
	// Flattened namespace: walk the unified store for records scoped to the
	// dying agent. Sole carrier → orphan the record; co-carried → just drop
	// this agent from the scope list (the other agents keep the tool).
	var orphans []OrphanedTempTool
	for _, p := range LoadPersistentTempTools(db, owner) {
		if !p.ScopedToAgent(agent.ID) {
			continue
		}
		if len(p.ScopeAgents) == 1 {
			orphans = append(orphans, OrphanedTempTool{
				Tool:            p.Tool,
				FormerAgentID:   agent.ID,
				FormerAgentName: agent.Name,
				OrphanedAt:      time.Now(),
			})
			_ = DeletePersistentTempTool(db, owner, p.Tool.Name)
			continue
		}
		kept := make([]string, 0, len(p.ScopeAgents)-1)
		for _, id := range p.ScopeAgents {
			if id != agent.ID {
				kept = append(kept, id)
			}
		}
		SetUserToolScopeAgents(db, owner, p.Tool.Name, kept)
	}
	if len(orphans) > 0 {
		AddOrphanedTempTools(db, owner, orphans)
		Log("[temptool.scope] captured %d orphaned tool(s) from deleted agent %q", len(orphans), agent.Name)
	}
}

// agentUserDB resolves the per-user orchestrate agent store from the
// admin's DB handle + owning username — the same path the admin's
// read side walks (UserDB(db.Bucket("orchestrate"), owner)).
func agentUserDB(db Database, owner string) Database {
	if db == nil || owner == "" {
		return nil
	}
	return UserDB(db.Bucket("orchestrate"), owner)
}

// resolveAgentOwner finds which user's store holds the given agent id by
// scanning every user — used when the admin attach form submits an agent
// id without its owning user. Returns "" when no store carries the id.
func resolveAgentOwner(db Database, agentID string) string {
	for _, u := range AuthListUsers(db) {
		udb := agentUserDB(db, u.Username)
		if udb == nil {
			continue
		}
		if _, ok := loadAgent(udb, agentID); ok {
			return u.Username
		}
	}
	return ""
}

// attachGlobalToolToAgent makes a global tool reachable by one agent.
// Additive and non-destructive, and it clears BOTH gates in one save:
//  1. If the tool is opted out on this agent (DisabledPersistentTools),
//     drop it from the deny-list — the tool flows back in.
//  2. AND if the agent carries a user-crafted allow-list (non-empty,
//     non-sentinel AllowedTools), append the tool so its restricted view
//     now includes it.
//  3. Otherwise the agent already sees the whole pool (nil/empty allow-list) —
//     nothing more to do. We deliberately do NOT create an allow-list here;
//     that would flip the agent from "sees everything" to "sees only this one".
//
// Steps 1 and 2 used to be alternatives, and that was the bug: agentSeesGlobalTool
// requires the deny-list to be silent AND the allow-list to name the tool, so an
// agent that had both a deny entry and an allow-list came back OFF no matter how
// many times the pill was switched on — it returned 204, and the pill was
// unchecked again on the next load.
func attachGlobalToolToAgent(db Database, owner, agentID, toolName string) error {
	if db == nil || agentID == "" || toolName == "" {
		return fmt.Errorf("attach requires db, agent, and tool")
	}
	// The admin attach form submits only the agent id — resolve its owning
	// user by scanning every user's store when the caller didn't supply one.
	if owner == "" {
		owner = resolveAgentOwner(db, agentID)
		if owner == "" {
			return fmt.Errorf("no owner found for agent %q", agentID)
		}
	}
	udb := agentUserDB(db, owner)
	if udb == nil {
		return fmt.Errorf("no agent store for user %q", owner)
	}
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return fmt.Errorf("agent %q not found", agentID)
	}
	// A "no tools" sentinel is refused rather than silently un-pinned:
	// attaching a tool to an agent the user pinned to zero tools is
	// contradictory. Said out loud, because the alternative — accept the toggle
	// and store nothing — is indistinguishable from the bug above.
	if isNoToolsSentinel(rec.AllowedTools) {
		return fmt.Errorf("%q is set to no optional tools — turn tools on in its Tools modal before granting one here", agentName(rec))
	}
	changed := false
	// 1) Re-enable an explicitly opted-out tool.
	for i, n := range rec.DisabledPersistentTools {
		if n == toolName {
			rec.DisabledPersistentTools = append(rec.DisabledPersistentTools[:i], rec.DisabledPersistentTools[i+1:]...)
			changed = true
			break
		}
	}
	// 2) Add to a user-crafted allow-list.
	if len(rec.AllowedTools) > 0 {
		canon := canonicalToolName(toolName)
		listed := false
		for _, n := range rec.AllowedTools {
			if canonicalToolName(n) == canon {
				listed = true
				break
			}
		}
		if !listed {
			rec.AllowedTools = append(rec.AllowedTools, toolName)
			changed = true
		}
	}
	// 3) Default agent already sees the whole pool — nothing to do.
	if !changed {
		return nil
	}
	_, err := saveAgent(udb, rec)
	return err
}

// agentName is the agent's display name, falling back to its id — for error
// text a user reads, where "agent-1754…" is better than an empty string.
func agentName(rec AgentRecord) string {
	if n := strings.TrimSpace(rec.Name); n != "" {
		return n
	}
	return rec.ID
}
