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
	var globalDef *TempTool
	for _, p := range LoadPersistentTempTools(db, owner) {
		if p.Tool.Name == toolName {
			t := p.Tool
			globalDef = &t
			break
		}
	}
	st.Global = globalDef != nil

	agents := listAgents(udb, owner)
	// scopeTarget: only top-level, user-managed agents are tool-scope targets.
	// App-specific agents (Guide Author, Servitor Investigator, …) are Hidden
	// with curated kits, and sub-agents (OwnedBy) are managed via their parent —
	// neither belongs in the pill or the derived state.
	// App agents are excluded by IDENTITY (not the Hidden proxy, which leaks for
	// a VISIBLE app agent like Casefile's "Case Analyzer" was): their kit is
	// app-declared, so they're never a tool-scope target. Sub-agents (OwnedBy)
	// are managed via their parent.
	scopeTarget := func(a AgentRecord) bool { return !a.Hidden && a.OwnedBy == "" && !isAppAgent(a.ID) }
	agentHas := map[string]bool{}
	var scopedDef *TempTool
	for i := range agents {
		if !scopeTarget(agents[i]) {
			continue
		}
		for j := range agents[i].Tools {
			if agents[i].Tools[j].Name == toolName {
				agentHas[agents[i].ID] = true
				if scopedDef == nil {
					t := agents[i].Tools[j]
					scopedDef = &t
				}
			}
		}
	}
	if !st.Global && len(agentHas) == 0 {
		return st, false
	}
	def := globalDef
	if def == nil {
		def = scopedDef
	}
	if def != nil {
		st.Missing = missingDeps(*def)
	}
	for i := range agents {
		if !scopeTarget(agents[i]) {
			continue
		}
		on := agentHas[agents[i].ID]
		if st.Global {
			on = agentSeesGlobalTool(agents[i], toolName)
		}
		st.Agents = append(st.Agents, ToolScopeAgent{ID: agents[i].ID, Name: agents[i].Name, On: on})
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
	// OFF at agent scope. Snapshot the definition + the agent name BEFORE the
	// remove so that if this is the LAST agent carrying a non-global tool, we
	// can rehome it into the orphan pool instead of letting it vanish — same
	// survival guarantee agent-delete gives via captureOrphanedTools. Without
	// this, unselecting the final scope pill silently destroyed the only copy.
	def := scopedDefOf(udb, owner, toolName)
	formerName := target
	for _, ag := range st.Agents {
		if ag.ID == target {
			formerName = ag.Name
			break
		}
	}
	if err := unbundleAgentToolByID(udb, owner, target, toolName); err != nil {
		return err
	}
	// st.Global is false here (the global branch returned above), so a tool
	// with no remaining agent copy now lives nowhere: orphan it.
	if def != nil && scopedDefOf(udb, owner, toolName) == nil {
		AddOrphanedTempTools(db, owner, []OrphanedTempTool{{
			Tool:            *def,
			FormerAgentID:   target,
			FormerAgentName: formerName,
			OrphanedAt:      time.Now(),
		}})
		Log("[temptool.scope] orphaned %q after unselecting its last scope (was on %q)", toolName, formerName)
	}
	return nil
}

// promoteScopedToGlobal lifts an agent-scoped tool into the pool and strips
// every agent copy — the Global-pill ON transition from agent scope.
func promoteScopedToGlobal(db, udb Database, owner, toolName string) error {
	def := scopedDefOf(udb, owner, toolName)
	if def == nil {
		return fmt.Errorf("no agent copy of %q to promote", toolName)
	}
	if err := AdminPersistTempTool(db, owner, *def); err != nil {
		return err
	}
	for _, a := range listAgents(udb, owner) {
		for _, t := range a.Tools {
			if t.Name == toolName {
				if err := unbundleAgentToolByID(udb, owner, a.ID, toolName); err != nil {
					// Don't silently leave a scoped copy behind: that's the
					// "promoted to global but it came back on Case Analyzer" bug.
					Log("[temptool.scope] promote %q: strip from %q failed: %v", toolName, a.Name, err)
				}
				break
			}
		}
	}
	return nil
}

// demoteGlobalToScoped removes a tool from the pool and bundles a copy onto
// every agent that currently sees it (Global-pill OFF = descope to the ON
// agents). Disabled/denied agents are left without a copy.
func demoteGlobalToScoped(db, udb Database, owner, toolName string, st ToolScopeState) error {
	var def *TempTool
	for _, p := range LoadPersistentTempTools(db, owner) {
		if p.Tool.Name == toolName {
			t := p.Tool
			def = &t
			break
		}
	}
	if def == nil {
		return fmt.Errorf("%q is not a global tool", toolName)
	}
	landed := 0
	for _, ag := range st.Agents {
		if ag.On {
			if err := bundleAgentToolByID(udb, owner, ag.ID, *def); err != nil {
				Log("[temptool.scope] demote %q: could not bundle onto %q: %v", toolName, ag.Name, err)
			} else {
				landed++
			}
		}
	}
	// Global OFF with no agent to descope onto would otherwise hard-delete the
	// only copy. Orphan it instead so it stays re-homeable — same guarantee the
	// agent-scope OFF path gives.
	if landed == 0 {
		AddOrphanedTempTools(db, owner, []OrphanedTempTool{{
			Tool:       *def,
			OrphanedAt: time.Now(),
		}})
		Log("[temptool.scope] orphaned %q on global-OFF with no descope target", toolName)
	}
	return DeletePersistentTempTool(db, owner, toolName)
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

// scopedDefOf returns the definition of an agent-scoped tool from whichever
// agent currently carries it.
func scopedDefOf(udb Database, owner, toolName string) *TempTool {
	for _, a := range listAgents(udb, owner) {
		for _, t := range a.Tools {
			if t.Name == toolName {
				tt := t
				return &tt
			}
		}
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
	// credential/pipeline scope fix). Saving writes the user's per-user shadow.
	_ = owner
	replaced := false
	for i := range rec.Tools {
		if rec.Tools[i].Name == t.Name {
			rec.Tools[i] = t
			replaced = true
			break
		}
	}
	if !replaced {
		rec.Tools = append(rec.Tools, t)
	}
	_, err := saveAgent(udb, rec)
	return err
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
	_ = owner
	kept := rec.Tools[:0]
	found := false
	for _, tl := range rec.Tools {
		if tl.Name == toolName {
			found = true
			continue
		}
		kept = append(kept, tl)
	}
	if !found {
		return fmt.Errorf("tool %q is not bundled on agent %q", toolName, rec.Name)
	}
	rec.Tools = kept
	_, err := saveAgent(udb, rec)
	return err
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
	if db == nil || owner == "" || len(agent.Tools) == 0 {
		return
	}
	global := map[string]bool{}
	for _, p := range LoadPersistentTempTools(db, owner) {
		global[p.Tool.Name] = true
	}
	var orphans []OrphanedTempTool
	for _, t := range agent.Tools {
		if global[t.Name] {
			continue
		}
		orphans = append(orphans, OrphanedTempTool{
			Tool:            t,
			FormerAgentID:   agent.ID,
			FormerAgentName: agent.Name,
			OrphanedAt:      time.Now(),
		})
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
// Additive and non-destructive:
//  1. If the tool is opted out on this agent (DisabledPersistentTools),
//     drop it from the deny-list — the tool flows back in.
//  2. Else if the agent carries a user-crafted allow-list (non-empty,
//     non-sentinel AllowedTools), append the tool so its restricted view
//     now includes it.
//  3. Else the agent already sees the whole pool (nil/empty allow-list) —
//     no-op. We deliberately do NOT create an allow-list here; that would
//     flip the agent from "sees everything" to "sees only this tool".
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
	// 1) Re-enable an explicitly opted-out tool.
	for i, n := range rec.DisabledPersistentTools {
		if n == toolName {
			rec.DisabledPersistentTools = append(rec.DisabledPersistentTools[:i], rec.DisabledPersistentTools[i+1:]...)
			_, err := saveAgent(udb, rec)
			return err
		}
	}
	// 2) Add to a user-crafted allow-list. A "no tools" sentinel is left
	// alone — attaching a tool to an agent the user pinned to zero tools is
	// contradictory, so we no-op rather than silently un-pin it.
	if len(rec.AllowedTools) > 0 && !isNoToolsSentinel(rec.AllowedTools) {
		canon := canonicalToolName(toolName)
		for _, n := range rec.AllowedTools {
			if canonicalToolName(n) == canon {
				return nil // already allowed
			}
		}
		rec.AllowedTools = append(rec.AllowedTools, toolName)
		_, err := saveAgent(udb, rec)
		return err
	}
	// 3) Default agent already sees the whole pool — nothing to do.
	return nil
}
