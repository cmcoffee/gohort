package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

const (
	agentsTable = "orchestrate_agents"
)

// loadAgent fetches an agent by ID. Returns false when not found.
//
// Seed-ID resolution: if the user has saved a shadow record under a
// seed's stable ID (e.g. "seed-research"), the shadow wins. Otherwise
// the in-code default from seedAgents() is returned with Owner =
// seedOwner so callers can detect "this is a virgin seed". Callers
// that want to know whether the result came from DB vs. in-code can
// check `Owner == seedOwner`.
func loadAgent(db Database, id string) (AgentRecord, bool) {
	var a AgentRecord
	if db == nil || id == "" {
		return a, false
	}
	// Builder special-case: the in-code seed is always authoritative
	// for the structural surface (persona, AllowedTools, DisableExplicit,
	// DisableInferred, IngestAttachments, MaxWorkerRounds, etc.). A
	// persisted shadow only contributes the user-curated `Rules`
	// field — which IS legitimate deployment customization the user
	// might add via the Rules modal. Everything else flows from the
	// current code defaults so prompt updates + new flags reach
	// existing deployments without manual revert. Matches Builder's
	// "locked from edits" UI posture.
	if id == "seed-builder" {
		seed, ok := seedAgentByID(id)
		if !ok {
			return a, false
		}
		var shadow AgentRecord
		if db.Get(agentsTable, id, &shadow) {
			applyBuilderDeploymentState(&seed, shadow)
		}
		return seed, true
	}
	// Other seeds (seed-chat, seed-research, ...): the framework owns the
	// PROMPT; the deployment owns operational state. A shadow gets created
	// the moment a user approves a tool for the agent (the approval path
	// persists an expanded AllowedTools list, see the seed-chat tool-enable
	// helper above) or saves Rules. The OLD behavior had that shadow win
	// ENTIRELY, which froze the OrchestratorPrompt at that instant, so
	// framework prompt updates never reached the deployment (the symptom:
	// a flat input-token count across redeploys even after prompt edits).
	// We now keep the shadow as the BASE (preserving AllowedTools, Rules,
	// think budget, attached skills/collections, exposure, etc.) and ALWAYS
	// refresh the prompt-bearing fields from the in-code seed, so prompt
	// improvements land without discarding the user's customizations. A
	// seed's OrchestratorPrompt is never user-editable in place (clone_agent
	// is the path for that), so this only ever replaces a stale framework
	// prompt with the current one. Builder above is the stricter sibling:
	// fully locked, so it rebases everything except Rules onto code.
	if seed, ok := seedAgentByID(id); ok {
		var shadow AgentRecord
		if db.Get(agentsTable, id, &shadow) {
			shadow.OrchestratorPrompt = seed.OrchestratorPrompt
			shadow.Description = seed.Description
			// Mode defines the agent's TYPE (chat vs orchestrator) — it's
			// framework-owned operational state, not a user customization, so
			// it MUST refresh from the seed. A minimal shadow created by a
			// tool-approval has Mode=="" and would otherwise silently demote
			// the Operator to a plain chat agent (the pinned-thread pin and
			// the orchestrator nav both gate on Mode).
			shadow.Mode = seed.Mode
			// Channel + Fleet are framework-owned TYPE flags too (same
			// rationale as Mode): a minimal tool-approval shadow has them
			// false and would otherwise silently strip seed-chat's channel
			// thread + fleet tools. Refresh from the seed. Per-agent override
			// of these on a seed is deferred toggle-persistence work; clone
			// for a different stance.
			shadow.Cortex = seed.Cortex
			shadow.Fleet = seed.Fleet
			// PreMortem is a framework-owned behavior flag (no user toggle; a
			// code-owned default for orchestrator seeds), so it refreshes from the
			// seed too — otherwise an existing shadow (created by a tool-approval
			// before this flag existed) never picks it up and the plan-first
			// behavior silently doesn't land after redeploy.
			shadow.PreMortem = seed.PreMortem
			// App-agents (registered via RegisterAppAgent, e.g. Casefile's
			// "Case Analyzer") are framework-owned for VISIBILITY too — the app
			// decides Hidden, not the user. A stale shadow, created the moment a
			// tool got mis-scoped onto the app-agent (the bundle path's
			// saveAgent), otherwise pins Hidden at whatever it was then, so
			// flipping the spec to Hidden:true never takes: the app-agent keeps
			// showing in the fleet picker and the scope pills. Refresh from the
			// spec so the app's decision wins (mirrors the prompt/Mode refresh).
			// Regular seeds keep their shadow Hidden — a user CAN flip a normal
			// agent's Hide toggle, and that's legitimate deployment state.
			if _, isApp := appagents.AppAgentByID(id); isApp {
				shadow.Hidden = seed.Hidden
			}
			shadow = selfHealAllowedTools(db, shadow)
			return enforceSubAgentPosture(applyLegacyMode(shadow)), true
		}
		// No shadow exists: return the framework default.
		return enforceSubAgentPosture(applyLegacyMode(seed)), true
	}
	// Non-seed (user-created / cloned) agent: the DB record is authoritative.
	if db.Get(agentsTable, id, &a) {
		a = selfHealAllowedTools(db, a)
		a = enforceSubAgentPosture(applyLegacyMode(a))
		return a, true
	}
	return a, false
}

// applyLegacyMode maps the retired Mode == "orchestrator" agent type onto
// the independent Channel + Fleet flags, so pre-split records — Operator
// shadows and agents cloned from the Operator — keep working until the
// one-time migration rewrites them. New code never sets Mode; it reads
// Channel and Fleet. Idempotent: setting both flags true again is a no-op.
func applyLegacyMode(a AgentRecord) AgentRecord {
	if a.Mode == "orchestrator" {
		a.Cortex = true
		a.Fleet = true
	}
	return a
}

// enforceSubAgentPosture pins the structural "sub-agent" fields when
// OwnedBy is set. A sub-agent is a focused capability component called
// by its parent via dispatch — not a user-facing standalone surface —
// so certain fields are meaningless or actively harmful and we ignore
// the stored value:
//
//   - Hidden    forced true:  sub-agents must not appear in the global
//     fleet "Available agents" prompt block; they're reachable only via
//     the parent's implicit dispatch authority.
//   - Exposed   forced false: sub-agents have no public /agents/ surface
//   - PublicName    cleared:  same reason
//   - AllowExplorer  → false: explorer mode is an interactive recovery
//     valve; sub-agent dispatches are focused single-task runs
//   - IntakeForm    cleared:  sub-agents receive structured input from
//     the parent, not from a user filling in a form
//   - DisableExplicit / DisableInferred → true ONLY for stateless specialists.
//     A plain dispatched sub-agent is one-shot, so accumulated facts / Reference
//     Memory can't be meaningfully scoped and would contaminate fresh lookups.
//     BUT a parent-inheriting sub-agent (InheritParentTools) is the persistent,
//     often SCHEDULED kind — e.g. a "summarize between time periods" agent that
//     must remember its last checkpoint across runs — so it KEEPS both memory
//     layers (forced ON here, overriding any stale stored disable).
//
// Think is left untouched — it's a legitimate per-agent author choice.
// Posture is enforced at the runtime read path so even if the stored
// record drifts (Builder mistake, manual DB edit, old data) the runtime
// treats sub-agents correctly. Editor + Builder also discipline the
// write path so wrong values don't end up persisted in the first place.
// agentParentExists reports whether a sub-agent's OwnedBy parent still exists —
// either an in-code seed (may have no stored shadow) or a stored agent record.
// Used to detect orphaned sub-agents (parent gone) so they can be promoted.
func agentParentExists(db Database, parentID string) bool {
	if strings.TrimSpace(parentID) == "" {
		return false
	}
	if _, isSeed := seedAgentByID(parentID); isSeed {
		return true
	}
	return db.Get(agentsTable, parentID, &AgentRecord{})
}

func enforceSubAgentPosture(a AgentRecord) AgentRecord {
	if a.OwnedBy == "" {
		return a
	}
	a.Hidden = true
	a.Exposed = false
	a.PublicName = ""
	a.AllowExplorer = false
	a.IntakeForm = nil
	if a.InheritParentTools {
		// Stateful inheriting sub-agent: memory ON so it can persist state
		// (a checkpoint) between scheduled runs.
		a.DisableExplicit = false
		a.DisableInferred = false
	} else {
		a.DisableExplicit = true
		a.DisableInferred = true
	}
	return a
}

// enableApprovedToolOnSeedChat is the OnTempToolApproved hook target.
// Seed-chat now uses DisabledPersistentTools as its sole opt-out lever;
// AllowedTools stays nil so every newly approved tool auto-appears
// without any per-tool enable step. This function's only remaining job
// is to log when a re-approved tool is staying suppressed because the
// user explicitly disabled it in the past.
func enableApprovedToolOnSeedChat(db Database, username, toolName string) {
	if db == nil || username == "" || toolName == "" {
		return
	}
	udb := UserDB(db, username)
	if udb == nil {
		return
	}
	var rec AgentRecord
	if !udb.Get(agentsTable, "seed-chat", &rec) {
		return // no shadow; tool auto-loads via default pool
	}
	for _, n := range rec.DisabledPersistentTools {
		if n == toolName {
			Log("[orchestrate.agents] approved tool %q stays disabled on seed-chat for user=%s (on user deny list)", toolName, username)
			return
		}
	}
	// Tool is approved and not on the deny list — it auto-loads at
	// runtime and the modal renders it checked (AllowedTools=nil means
	// all-on, minus DisabledPersistentTools). Nothing to write.
}

// migrateBuilderShadows is the one-shot startup migration that
// eagerly applies the loadAgent("seed-builder") overlay to every
// user's persisted shadow. Without it, shadows from before the
// Builder lockdown carry stale fields (old prompt, missing
// DisableExplicit/DisableInferred/IngestAttachments flags, old AllowedTools) — the
// lazy read path returns the right thing, but the DB rows still
// hold dead values for anyone inspecting them directly.
//
// Walks AuthDB for the user list, opens each user's per-user
// sub-store via UserDB, and for any user with a seed-builder
// shadow re-writes it with the current in-code seed (preserving
// Rules). Idempotent — running again produces the same record.
// applyBuilderDeploymentState carries the fields a rebase onto the in-code
// Builder seed must NOT discard, from the user's persisted shadow onto the
// seed. Everything absent from this list (prompt, AllowedTools, the authoring
// kit, round budgets) deliberately flows from code so framework updates reach
// existing deployments without a manual revert.
//
// The distinction is authorship: framework STRUCTURE is ours, deployment
// DECISIONS are the owner's. A denied credential, a bundled tool, a rulebook,
// and which model this deployment runs Builder on are all the owner's answers
// to questions the seed doesn't get a vote on — and each one that fell off this
// list showed up as a control that silently refused to stick.
//
// One list, used by BOTH the read path and the startup migration. They had
// drifted: the migration preserved only Rules, so every restart wrote the
// seed's empty scope fields over the shadow that loadAgent then read back
// from — the read path was carefully preserving state the boot path had
// already destroyed.
func applyBuilderDeploymentState(seed *AgentRecord, shadow AgentRecord) {
	if seed == nil {
		return
	}
	if r := strings.TrimSpace(shadow.Rules); r != "" {
		seed.Rules = shadow.Rules
	}
	// Scope decisions — denying a credential / pipeline / tool on Builder, or
	// bundling one onto it via the scope pill / add_tool.
	seed.DisabledCredentials = shadow.DisabledCredentials
	seed.DisabledPipelines = shadow.DisabledPipelines
	seed.AttachedPipelines = shadow.AttachedPipelines
	seed.DisabledPersistentTools = shadow.DisabledPersistentTools
	seed.Tools = shadow.Tools
	// Which model Builder reasons on. Builder stopped being special here when
	// orchestratorRouteKey dropped its dedicated always-lead stage: the "Use
	// Lead model" toggle is now the ONE control that decides it, and the editor
	// shows that toggle on Builder like any other agent. Left off this list it
	// saved and then read back false every time, so the toggle appeared to
	// refuse to turn on.
	seed.LeadModel = shadow.LeadModel
	// The owner's enforced limits. These are deployment state in the strictest
	// sense — the framework owns Builder's PROMPT, the deployment owns what it
	// is allowed to do — and they are owner-only fields no agent edit path can
	// reach, so rebasing them onto code protects nothing and destroys the one
	// thing the owner wrote by hand.
	//
	// Left off this list they saved and read back empty, silently: the
	// guardrails endpoint wrote them to the shadow, loadAgent rebuilt Builder
	// from the seed without them, and migrateBuilderShadows then wrote that
	// stripped copy BACK over the shadow at boot, so a restart erased them for
	// good. The visible symptom was "exceptions won't save"; the real one was
	// that guardrails never applied to Builder AT ALL — no rules, no hooks, no
	// declines — which is the agent with authoring access and therefore the one
	// an owner is most likely to want limits on.
	seed.Guardrails = shadow.Guardrails
	seed.GuardrailHooks = shadow.GuardrailHooks
	seed.GuardrailFailClosed = shadow.GuardrailFailClosed
	seed.GuardrailDeclines = shadow.GuardrailDeclines
	seed.GuardrailsDisabled = shadow.GuardrailsDisabled
	seed.GuardrailExceptions = shadow.GuardrailExceptions
	seed.AuthorizedIdentities = shadow.AuthorizedIdentities
}

func (T *OrchestrateApp) migrateBuilderShadows() {
	if T == nil || T.DB == nil || AuthDB == nil {
		return
	}
	authDB := AuthDB()
	if authDB == nil {
		return
	}
	seed, ok := seedAgentByID("seed-builder")
	if !ok {
		return
	}
	migrated := 0
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(T.DB, u.Username)
		if udb == nil {
			continue
		}
		var shadow AgentRecord
		if !udb.Get(agentsTable, "seed-builder", &shadow) {
			continue
		}
		merged := seed
		applyBuilderDeploymentState(&merged, shadow)
		merged.Updated = time.Now()
		udb.Set(agentsTable, "seed-builder", merged)
		migrated++
		Log("[orchestrate.migrate] re-applied seed-builder defaults for user=%q (rules=%v lead_model=%v scoped_tools=%d)",
			u.Username, merged.Rules != "", merged.LeadModel, len(merged.Tools))
	}
	if migrated > 0 {
		Log("[orchestrate.migrate] migrateBuilderShadows: refreshed %d user shadow(s)", migrated)
	}
}

// dropLegacyOperator is a one-shot migration that deletes the retired
// Operator seed. The Operator folded into Chat (seed-chat), so it was
// removed from seedAgents() — but any per-user shadow record (minted
// back when seed-operator was a live seed, by customization or
// tool-approval) still lists as "Operator" in the agent menu, because
// seedAgentByID("seed-operator") is now false and listAgents emits
// unknown owned records verbatim. This wipes that shadow per user:
// the record, its session bucket (including the old "operator-thread"
// home thread, which lived under orchestrate_sessions:seed-operator),
// and the per-(user, agent) memory + knowledge. Done directly rather
// than via deleteAgent so a legacy record with a non-matching Owner
// field can't trip the ownership guard — we're already inside each
// user's own store. Idempotent: users without the shadow are skipped.
func (T *OrchestrateApp) dropLegacyOperator() {
	if T == nil || T.DB == nil || AuthDB == nil {
		return
	}
	authDB := AuthDB()
	if authDB == nil {
		return
	}
	dropped := 0
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(T.DB, u.Username)
		if udb == nil {
			continue
		}
		if !udb.Get(agentsTable, "seed-operator", &AgentRecord{}) {
			continue
		}
		dropChatSessionBucket(udb, "seed-operator")
		udb.Unset(agentsTable, "seed-operator")
		dropAgentSideData(udb, u.Username, "seed-operator")
		dropped++
		Log("[orchestrate.migrate] dropLegacyOperator: removed retired Operator for user=%q", u.Username)
	}
	if dropped > 0 {
		Log("[orchestrate.migrate] dropLegacyOperator: removed %d Operator shadow(s)", dropped)
	}
}

// deployMigrationsTable holds deployment-wide (not per-user) one-shot migration
// markers, keyed by a migration id. Distinct from the per-user
// orchestrate_migrations markers.
const deployMigrationsTable = "deploy_migrations"

// migrateGlobalToolAdoption grandfathers every existing user into the global-
// tool OPT-IN model exactly once. Before it, every Shared tool auto-loaded for
// every user; now a Shared tool loads for a user only after they adopt it from
// the catalog. To avoid silently pulling tools out from under people, this seeds
// each existing user's adoption list with the current shared-tool names. A
// deployment-wide marker makes it run once — a user who later unadopts
// everything is never re-seeded, and users created after the marker start empty
// (true opt-in). See LoadAdoptedGlobalTools + the runner's shared-pool load.
func (T *OrchestrateApp) migrateGlobalToolAdoption() {
	if T == nil || AuthDB == nil {
		return
	}
	authDB := AuthDB()
	store := RootDB
	if authDB == nil || store == nil {
		return
	}
	const marker = "global_tool_adoption_v1"
	var done bool
	store.Get(deployMigrationsTable, marker, &done)
	if done {
		return
	}
	shared := LoadSharedPersistentTempTools(store)
	names := make([]string, 0, len(shared))
	for _, p := range shared {
		names = append(names, p.Tool.Name)
	}
	if len(names) > 0 {
		users := AuthListUsers(authDB)
		for _, u := range users {
			MergeAdoptedGlobalTools(store, u.Username, names)
		}
		Log("[orchestrate.migrate] global-tool opt-in: grandfathered %d shared tool(s) for %d existing user(s)", len(names), len(users))
	}
	store.Set(deployMigrationsTable, marker, true)
}

// migrateSeedChatFrozenAllowedTools clears the AllowedTools field on
// every user's seed-chat shadow that was materialized by the old
// enableApprovedToolOnSeedChat expansion path. The old code froze an
// explicit snapshot on first tool-approval; tools enabled via non-standard
// paths (toolbox enables, agency menu) were absent from the snapshot and
// filtered at runtime. Resetting to empty restores the default-pool
// sentinel so all approved persistent tools auto-load. Idempotent —
// shadows already at empty (or no shadow at all) are skipped.
func (T *OrchestrateApp) migrateSeedChatFrozenAllowedTools() {
	if T == nil || T.DB == nil || AuthDB == nil {
		return
	}
	authDB := AuthDB()
	if authDB == nil {
		return
	}
	cleared := 0
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(T.DB, u.Username)
		if udb == nil {
			continue
		}
		var shadow AgentRecord
		if !udb.Get(agentsTable, "seed-chat", &shadow) {
			continue
		}
		// Skip only when both fields are already clean. The first migration
		// run may have cleared AllowedTools but not DisabledPersistentTools
		// (before that clear was added), so we can't stop at AllowedTools==nil.
		alreadyClean := (len(shadow.AllowedTools) == 0 || isNoToolsSentinel(shadow.AllowedTools)) &&
			len(shadow.DisabledPersistentTools) == 0
		if alreadyClean {
			continue
		}
		if !isNoToolsSentinel(shadow.AllowedTools) {
			shadow.AllowedTools = nil
		}
		// DisabledPersistentTools was populated by the frozen-list save path
		// (tools absent from the snapshot were written to the deny list).
		shadow.DisabledPersistentTools = nil
		shadow.Updated = time.Now()
		udb.Set(agentsTable, "seed-chat", shadow)
		cleared++
		Log("[orchestrate.migrate] migrateSeedChatFrozenAllowedTools: reset seed-chat for user=%q", u.Username)
	}
	if cleared > 0 {
		Log("[orchestrate.migrate] migrateSeedChatFrozenAllowedTools: cleared %d frozen AllowedTools snapshot(s)", cleared)
	}
}

// migrateAgentPersistentTools snapshots persistent-pool tools into
// every existing agent's Tools[] when the agent's AllowedTools names
// them. One-shot eager version of the auto-snapshot now baked into
// autoCopySessionToolsForAgent — closes the gap for agents created
// before the copy-always change went in.
//
// Walks AuthDB for users, opens each user's per-user store via
// UserDB, iterates agent records. Idempotent: snapshotted names are
// detected and skipped on re-run. Builder is skipped — its Tools[]
// is managed by the overlay path, not user state.
func (T *OrchestrateApp) migrateAgentPersistentTools() {
	// NEUTERED by the namespace flatten: this pre-flatten migration COPIED
	// pool tools into AgentRecord.Tools — under the unified store that would
	// recreate the exact duplicate-homes problem the flatten removes (the
	// lazy fold would then merge them back out, churning every record).
	// Kept as a stub so the call site and history stay legible.
}

// migrateLegacyOrchestratorMode rewrites every remaining Mode=="orchestrator"
// agent record into the split Cortex + Fleet flags and clears the marker, so
// applyLegacyMode stops re-forcing Cortex=Fleet=true on every load. Without
// this, a legacy record's Fleet flag could never be turned off (and the agent
// never published) until it was re-saved by hand. Preserves the effective
// behavior the marker produced (both flags on); the owner can then toggle
// Fleet off and have it stick. Runs once, deployment-wide, via the migration
// runner (so it shows in the admin Migrations table and never re-runs).
func (T *OrchestrateApp) migrateLegacyOrchestratorMode() {
	NewMigrationRunner("orchestrate", "").Once("clear_legacy_orchestrator_mode:v1", func() int {
		if T.DB == nil || AuthDB == nil {
			return 0
		}
		authDB := AuthDB()
		if authDB == nil {
			return 0
		}
		changed := 0
		for _, u := range AuthListUsers(authDB) {
			udb := UserDB(T.DB, u.Username)
			if udb == nil {
				continue
			}
			for _, k := range udb.Keys(agentsTable) {
				var a AgentRecord
				if !udb.Get(agentsTable, k, &a) || a.Mode != "orchestrator" {
					continue
				}
				a.Cortex = true
				a.Fleet = true
				a.Mode = ""
				udb.Set(agentsTable, k, a)
				changed++
			}
		}
		return changed
	})
}

// noToolsSentinel is the reserved AllowedTools[0] marker meaning
// "admin explicitly disabled all optional tools." The framework
// distinguishes this from a bare empty list (which means "use the
// default pool") so the user's intent survives a save → reload cycle
// in the Tools modal. The actual string is irrelevant; "__none__"
// reads well in JSON and is unlikely to collide with a real tool name.
const noToolsSentinel = "__none__"

// isNoToolsSentinel reports whether AllowedTools is the explicit
// no-optional-tools marker. Exported via the package so runner.go's
// resolveWorkerTools can short-circuit before the default-pool
// expansion.
func isNoToolsSentinel(allowed []string) bool {
	return len(allowed) == 1 && allowed[0] == noToolsSentinel
}

// Dispatch policy modes — how an agent's AllowedDispatchTargets list is read.
// See AgentRecord.DispatchMode. Resolve with effectiveDispatchMode, never the
// raw field, so back-compat inference + the deleted-target self-heal apply.
const (
	dispatchAll    = "all"    // any non-hidden agent (default)
	dispatchOnly   = "only"   // allowlist: only the listed agents
	dispatchExcept = "except" // denylist: any non-hidden agent EXCEPT the listed
	dispatchNone   = "none"   // no dispatch at all
)

// effectiveDispatchMode resolves an agent's dispatch policy, applying back-
// compat: a blank DispatchMode with a non-empty AllowedDispatchTargets is the
// legacy allowlist ("only"); blank with an empty list is "all". An unrecognized
// value degrades to "all" (fail-open to the default, never a silent hard block).
func effectiveDispatchMode(a AgentRecord) string {
	switch a.DispatchMode {
	case dispatchAll, dispatchOnly, dispatchExcept, dispatchNone:
		return a.DispatchMode
	default:
		if len(a.AllowedDispatchTargets) > 0 {
			return dispatchOnly
		}
		return dispatchAll
	}
}

// dispatchListContains reports whether id is in the agent's dispatch target list.
func dispatchListContains(a AgentRecord, id string) bool {
	for _, x := range a.AllowedDispatchTargets {
		if x == id {
			return true
		}
	}
	return false
}

// selfHealAllowedTools strips entries from AllowedTools that no
// longer resolve — either because the registered tool was removed
// (post-blocklist update / migration) or because a persistent temp
// tool referenced by name has been deleted. Cleaned record is
// persisted back so the orphan is gone for good on the next read.
// No-op when AllowedTools is empty (default-pool agents) or when
// nothing is stale. Also no-op when the no-tools sentinel is set —
// the marker isn't a registered tool name and would otherwise get
// stripped, silently reverting the agent to the default pool.
func selfHealAllowedTools(db Database, a AgentRecord) AgentRecord {
	if len(a.AllowedTools) == 0 || isNoToolsSentinel(a.AllowedTools) {
		return a
	}
	cleaned := a.AllowedTools[:0]
	dropped := false
	for _, name := range a.AllowedTools {
		if isResolvableToolName(db, a.Owner, name) {
			cleaned = append(cleaned, name)
			continue
		}
		Log("[orchestrate.agents] dropping stale tool %q from agent %q AllowedTools (not registered, not in owner's temp-tool pool)", name, a.ID)
		dropped = true
	}
	if !dropped {
		return a
	}
	// Healing away the LAST entry must not empty the list. An empty
	// AllowedTools reads as "sees the whole default pool" (see the guard at the
	// top of this function, and agentSeesGlobalTool), so an agent pinned to a
	// restricted list whose entries all went stale — e.g. its only temp tool was
	// deleted — would silently WIDEN from a few tools to every tool. Collapse to
	// the explicit no-tools sentinel instead: the restriction was deliberate, so
	// losing its last member means "nothing", never "everything".
	if len(cleaned) == 0 {
		Log("[orchestrate.agents] agent %q AllowedTools healed to empty; pinning no-tools sentinel rather than widening to the default pool", a.ID)
		a.AllowedTools = []string{noToolsSentinel}
	} else {
		a.AllowedTools = cleaned
	}
	a.Updated = time.Now()
	db.Set(agentsTable, a.ID, a)
	return a
}

// isResolvableToolName reports whether the given name maps to either
// a registered ChatTool, a connected gohort-desktop local tool, or
// one of the agent owner's persistent temp tools. Used to detect
// orphan entries left in AllowedTools after a tool gets unregistered
// or a temp tool gets deleted.
//
// Client-bridge tools (name prefix "from_client.") are treated as
// ALWAYS resolvable: they're framework-runtime tools injected
// per-turn from the desktop bridge regardless of whether the
// agent's AllowedTools lists them, and the desktop may be
// disconnected at AllowedTools-load time even when it's connected
// later at chat-turn time. Stripping them at load would create a
// thrash where the user toggles them on, the load self-heals them
// off, and the runtime keeps adding them via the per-turn hook
// anyway.
func isResolvableToolName(db Database, owner, name string) bool {
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "from_client.") {
		return true
	}
	// Legacy call_<credential> aliases resolve to fetch_url_<credential>.
	// Treat them as resolvable so AllowedTools entries from before the
	// 0.3.1 rename don't get stripped by self-heal. The agent loop's
	// lookup path applies the same translation. call_no_auth has no
	// counterpart — fetch_url covers it directly — so that legacy name
	// fails through and is healed away on first save.
	if strings.HasPrefix(name, "call_") && name != "call_no_auth" {
		return true
	}
	if _, ok := FindChatTool(name); ok {
		return true
	}
	if owner == "" || db == nil {
		return false
	}
	// Persistent temp tools live in RootDB keyed by username; the
	// LoadPersistentTempTools helper handles the lookup with the
	// canonical store regardless of which db we pass in.
	for _, p := range LoadPersistentTempTools(db, owner) {
		if p.Tool.Name == name {
			return true
		}
	}
	return false
}

// saveAgent upserts an agent record, stamping timestamps + ID on new
// records. Owner must be set by the caller. Seed-IDs are written
// under the same ID as user-owned shadow records (no forking) — this
// is what makes "Edit a seed, then Revert" work.
func saveAgent(db Database, a AgentRecord) (AgentRecord, error) {
	if db == nil {
		return a, fmt.Errorf("db not initialized")
	}
	if strings.TrimSpace(a.Name) == "" {
		return a, fmt.Errorf("name is required")
	}
	if strings.TrimSpace(a.OrchestratorPrompt) == "" {
		return a, fmt.Errorf("orchestrator_prompt is required")
	}
	// Builder-specific invariants. Hidden must stay true — Builder's
	// authoring flows require the user directly (one-question-at-a-
	// time intake, ask_user_form, draft sessions), none of which
	// survive a fleet dispatch. Even if a shadow edit tried to flip
	// it, the dispatch path's isBuilderAgent gate already refuses;
	// forcing it here keeps the record consistent with the runtime.
	if isBuilderAgent(a.ID) {
		a.Hidden = true
	}
	// Template seeds (Builder clones them; never run/published directly) must
	// never become Exposed. This ALSO repairs a stale shadow that the
	// auto-expose rule below wrongly flipped true in the past — checked first so
	// that rule can't re-expose it.
	if isCloneOnlySeed(a.ID) {
		a.Exposed = false
	} else if a.Hidden && !a.Exposed && newlyHidden(db, a) {
		// Reachability DEFAULT (not an invariant): a Hidden agent is
		// orphaned if it's also unexposed — hidden from the fleet AND
		// absent from the dashboard leaves the owner no surface to reach
		// it. So flipping Hide ON defaults Exposed ON.
		//
		// newlyHidden is what makes this a default instead of a cage. The
		// rule used to run on EVERY save, so a Hidden agent could never be
		// un-exposed: the same save that set Exposed=false immediately flipped
		// it back, and the dashboard card the user was trying to remove
		// reappeared every time. The comment here promised "they can still
		// manually turn Exposed off after" — the code made that impossible.
		//
		// Now it fires only on the transition into Hidden (or on create), so
		// the default still lands once and the user's later choice sticks.
		a.Exposed = true
	}
	// Drop the retired "orchestrator" mode marker on save. The record now
	// carries the split Cortex + Fleet flags explicitly (the form's toggles),
	// so applyLegacyMode must stop re-forcing Cortex=Fleet=true on every load —
	// which is exactly what kept a cloned-from-Operator cortex agent from ever
	// going Fleet-off, and therefore from being publishable. Saving the record
	// IS its one-time migration to the split model.
	a.Mode = ""
	now := time.Now()
	if a.ID == "" {
		a.ID = UUIDv4()
		a.Created = now
	}
	if a.Created.IsZero() {
		a.Created = now
	}
	a.Updated = now
	db.Set(agentsTable, a.ID, a)
	return a, nil
}

// listAgents returns agents visible to the given user — their own
// records plus every seed (merged with the user's shadow when one
// exists). Sorted by name for stable display. Each seed appears
// exactly once: shadowed seeds show the user's tweaks; un-shadowed
// seeds show the in-code defaults.
func listAgents(db Database, owner string) []AgentRecord {
	if db == nil {
		return nil
	}
	out := make([]AgentRecord, 0)
	seen := map[string]bool{}
	// Pass 1: walk the user's own records.
	for _, k := range db.Keys(agentsTable) {
		var a AgentRecord
		if !db.Get(agentsTable, k, &a) {
			continue
		}
		// Seed shadows: route through loadAgent so framework-owned fields
		// (prompt, description, Mode) are refreshed from the in-code seed
		// instead of frozen at whatever the shadow captured. Without this a
		// Mode-less shadow would hide the orchestrator nav for the Operator.
		// This MUST run before the seedOwner skip below: a shadow created by a
		// scope mutation on a virgin seed inherits the seed's Owner=seedOwner
		// marker (that marker is load-bearing elsewhere, so we don't rewrite
		// it), and culling it as "stale" would drop the user's scope decisions
		// (denied credential / pipeline / tool) and re-add the pristine seed —
		// the "can't unselect Builder/seed via api scope" bug.
		if _, isSeed := seedAgentByID(a.ID); isSeed {
			if merged, ok := loadAgent(db, a.ID); ok {
				out = append(out, merged)
				seen[a.ID] = true
				continue
			}
		}
		// Skip stale rows from the pre-shadow era when NON-seed records were
		// installed into per-user sub-stores with Owner=seedOwner.
		// Migration drops them on first list, but harden anyway.
		if a.Owner == seedOwner {
			continue
		}
		// Orphaned-sub-agent self-heal: OwnedBy points at a parent that no longer
		// exists (parent deleted before the cascade fix, cross-owner, legacy data).
		// A sub-agent is pinned Hidden, so an orphan is INVISIBLE and unmanageable —
		// promote it to a top-level agent (clear OwnedBy + un-hide) so it surfaces
		// and can be kept or deleted. Persisted once; after that it's a normal
		// agent and this never fires for it again.
		if a.OwnedBy != "" && !agentParentExists(db, a.OwnedBy) {
			a.OwnedBy = ""
			a.Hidden = false
			_, _ = saveAgent(db, a)
		}
		out = append(out, enforceSubAgentPosture(a))
		seen[a.ID] = true
	}
	// Pass 2: in-code seeds that the user hasn't shadowed. Adds the
	// framework default so every seed slot always has one entry in
	// the dropdown.
	for _, seed := range seedAgents() {
		if seen[seed.ID] {
			continue
		}
		out = append(out, enforceSubAgentPosture(seed))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// deleteAgent removes an agent. For seed-IDs the row is a shadow
// (user's customization); deleting it reverts the agent to the
// in-code default. For non-seed IDs the agent record, its session
// bucket, AND the per-(user, agent) memory + knowledge are all
// wiped — leaving those behind made the LLM see references to
// agents the user thought they'd deleted (memory notes prepended
// to every turn's prompt, knowledge chunks surfacing in semantic
// search, etc.). No-op on a virgin seed (no shadow row to remove).
func deleteAgent(db Database, id, owner string) error {
	orphaned, err := deleteAgentReporting(db, id, owner)
	// Fired from the top-level entry points rather than inside the recursive
	// body, so a cascade of sub-agent deletes queues one set of suggestions
	// for the whole operation instead of one per level.
	if err == nil {
		noteOrphanedToolMemory(db, owner, orphaned)
	}
	return err
}

// deleteAgentReporting is deleteAgent plus the names of any tools the delete
// took out of every catalog (the agent was their last carrier, so they went
// to the orphan pool). Cascaded sub-agent deletes contribute theirs too.
//
// The list exists because the drop was otherwise silent everywhere it
// mattered: the tool stopped being callable by ANY agent, no surface said so,
// and a model that had used it before went looking for other ways to reach
// it — inventing a shell invocation for a tool that no longer existed. A
// delete that removes capability has to say which capability it removed.
func deleteAgentReporting(db Database, id, owner string) ([]string, error) {
	if isSeedID(id) {
		// Shadow record (if any) is owned by the user; nothing to
		// guard since the user is mutating their own copy.
		if exists := db.Get(agentsTable, id, &AgentRecord{}); !exists {
			return nil, fmt.Errorf("agent %q is at framework defaults (nothing to revert)", id)
		}
		db.Unset(agentsTable, id)
		// A seed revert ALSO drops the user's accumulated memory +
		// knowledge under that agent — those were tied to the
		// customized persona the user is throwing away. Keeping
		// them would make the reverted-default agent inherit the
		// shadow's accumulated context, which contradicts "revert
		// to defaults".
		dropAgentSideData(db, owner, id)
		return nil, nil
	}
	a, ok := loadAgent(db, id)
	if !ok {
		return nil, fmt.Errorf("agent %q not found", id)
	}
	if a.Owner != owner {
		return nil, fmt.Errorf("agent %q is not yours", id)
	}
	// Agent-scoped tools live INSIDE this record, so they'd vanish with it.
	// Capture any that aren't also global into the owner's orphan pool so the
	// admin can re-home or discard them deliberately (Orphaned Tools surface).
	orphaned := captureOrphanedTools(db, owner, a)
	// Cascade-delete sub-agents — anything where OwnedBy points at the
	// agent being deleted. Recursive (a sub-agent that owns its own
	// sub-agents propagates the delete down). Idempotent: a sub-agent
	// already gone (manual delete prior) is just skipped. Owned
	// children's session buckets, memory, knowledge get cleaned up by
	// the recursive deleteAgent call's normal path.
	for _, k := range db.Keys(agentsTable) {
		if k == id {
			continue
		}
		var child AgentRecord
		if !db.Get(agentsTable, k, &child) {
			continue
		}
		if child.OwnedBy == id && child.Owner == owner {
			Log("[orchestrate.agents] cascade-deleting sub-agent %q (owned_by=%q)", child.Name, a.Name)
			childOrphans, _ := deleteAgentReporting(db, child.ID, owner)
			orphaned = append(orphaned, childOrphans...)
		}
	}
	// Clear cross-references so a deleted agent doesn't dangle in the fleet:
	// channels that route to it, monitors / standing agents that wake it, and —
	// importantly — every OTHER agent's dispatch allowlist that names it. A stale
	// id left in an allowlist keeps that agent in restrict-mode, which hides all
	// NON-listed agents (so a freshly added/imported agent silently won't appear
	// as available). Channels/monitors/standing agents live in RootDB.
	for _, ch := range ListChannelsForAgent(RootDB, owner, id) {
		DeleteChannel(RootDB, owner, ch.ID)
	}
	// Monitors / standing agents that target the deleted agent are NOT removed —
	// silently losing them is the friction we're avoiding. Mark them broken +
	// paused (cancels their live schedule) so they survive, show a "needs relink"
	// state in the console, and can be re-pointed at a live agent or deleted
	// deliberately.
	for _, m := range ListEventMonitors(RootDB, owner) {
		if m.WakeAgent == id {
			MarkEventMonitorBroken(RootDB, owner, m.Name,
				fmt.Sprintf("wakes deleted agent %q", a.Name))
		}
	}
	for _, s := range ListStandingAgents(RootDB, owner) {
		if s.AgentID == id {
			MarkStandingAgentBroken(RootDB, owner, s.Name,
				fmt.Sprintf("runs deleted agent %q", a.Name))
		}
	}
	// Recurring tasks have no stored record — they live only as scheduler
	// entries — so "keep, don't drop" means cancelling the live entry and re-arming
	// a dormant broken one (parkRecurringBroken) rather than a mark-in-place.
	for _, row := range listAgentRecurringTasks(owner, id) {
		UnscheduleTask(row.TaskID)
		parkRecurringBroken(row.Payload, fmt.Sprintf("its agent %q was deleted", a.Name))
	}
	for _, k := range db.Keys(agentsTable) {
		if k == id {
			continue
		}
		var other AgentRecord
		if !db.Get(agentsTable, k, &other) || other.Owner != owner || len(other.AllowedDispatchTargets) == 0 {
			continue
		}
		var kept []string
		changed := false
		for _, t := range other.AllowedDispatchTargets {
			if t == id {
				changed = true
				continue
			}
			kept = append(kept, t)
		}
		if changed {
			other.AllowedDispatchTargets = kept
			_, _ = saveAgent(db, other)
		}
	}
	dropChatSessionBucket(db, id)
	db.Unset(agentsTable, id)
	dropAgentSideData(db, owner, id)
	if len(orphaned) > 0 {
		Warn("[orchestrate.agents] deleting %q left %d tool(s) callable by NO agent — %s. Re-home them in Admin › Orphaned Tools or they stay dark.",
			a.Name, len(orphaned), strings.Join(orphaned, ", "))
	}
	return orphaned, nil
}

// dropAgentSideData wipes per-(user, agent) state that lives outside
// the AgentRecord + sessions bucket. Called on full delete (record +
// state goes) and seed revert (the shadow's state was specific to
// the customized version, doesn't belong to the framework default).
//
// Four stores get cleaned:
//
//   - Memory facts: MemoryFactsTable namespace "agent:<agent_id>" in
//     the per-user db — live rows AND tombstones. Without this an
//     agent delete stranded the whole fact store, and recreating an
//     agent under the same ID resurrected the old one's memory.
//   - Entity graph: GraphEntityTable/GraphEdgeTable under the same
//     namespace (populated by link_entities + auto-extraction).
//   - Knowledge topics accumulator: orchestrate_knowledge_topics
//     keyed by "<user>:<agent_id>".
//   - Embedded chunks: EmbeddedChunks rows with Source starting with
//     "orchestrate:<user>:<agent_id>" (every topic-suffixed variant
//     belongs to this agent). Scanned in one pass against AuthDB
//     since chunks live in the deployment-wide vector store.
func dropAgentSideData(db Database, owner, agentID string) {
	if db == nil || owner == "" || agentID == "" {
		return
	}
	key := owner + ":" + agentID
	ns := factsNamespace(agentID)
	if n := WipeMemoryFactNamespace(db, ns); n > 0 {
		Log("[orchestrate.agents] dropped %d memory fact(s) for deleted agent %s/%s", n, owner, agentID)
	}
	if ents, edges := WipeGraphNamespace(db, ns); ents+edges > 0 {
		Log("[orchestrate.agents] dropped graph for deleted agent %s/%s (%d entities, %d edges)", owner, agentID, ents, edges)
	}
	db.Unset(knowledgeTopicsTable, key)

	// Knowledge chunks live in AuthDB (the deployment-wide root)
	// because the vector index is shared across apps. Scan its
	// EmbeddedChunks table for any chunk whose Source belongs to
	// this (user, agent) and remove them. Cheap at gohort scale
	// (table walked once on delete, not on every read).
	authDB := db
	if AuthDB != nil {
		authDB = AuthDB()
	}
	if authDB == nil {
		return
	}
	prefix := knowledgeSource(owner, agentID, "")
	// Legacy agent-shared bucket — removed as a live surface but
	// still wiped on agent delete to clean up any stranded chunks
	// from before the move to attached collections.
	sharedPrefix := "agent-shared:" + agentID
	removed := 0
	for _, k := range authDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !authDB.Get(EmbeddedChunks, k, &c) {
			continue
		}
		// Match either the bare per-(user, agent) source OR any
		// topic-suffixed variant. Both forms share the prefix. Also
		// wipe the admin-curated agent-shared bucket — when the agent
		// itself is deleted, its shared KB has nowhere to live.
		if c.Source == prefix || strings.HasPrefix(c.Source, prefix+":") || c.Source == sharedPrefix {
			authDB.Unset(EmbeddedChunks, k)
			removed++
		}
	}
	if removed > 0 {
		Log("[orchestrate.agents] dropped %d knowledge chunk(s) for deleted agent %s/%s", removed, owner, agentID)
	}
}

// isSeedID reports whether the given ID belongs to a framework-defined
// seed. Used at storage boundaries to switch between "user record"
// and "shadow / revert-to-default" semantics.
func isSeedID(id string) bool {
	_, ok := seedAgentByID(id)
	return ok
}

// fleetHidden reports an agent that must not appear on ANY discovery surface —
// not a picker, not a dispatch list, and not the Builder's survey.
//
// Four separate reasons converge on the same answer, and they had been spelled
// out inline at each call site with different subsets: some checked all four,
// the dispatch-discovery paths checked only the two seed predicates. That is
// how a Hidden app agent (the Servitor Investigator, a per-appliance TEMPLATE)
// and the retired Chat / Research / Knowledge Base seeds all turned up in a
// survey of the fleet, presented to the Builder as things it could reuse or
// dispatch to.
//
// One predicate so the answer cannot differ by surface. Contextual exclusions
// (self, Builder) stay at their call sites — those depend on who is asking.
func fleetHidden(id string) bool {
	return hiddenAppAgent(id) || isCloneOnlySeed(id) ||
		isFleetRetiredSeed(id) || isRetiringArchetypeSeed(id)
}

// isFleetRetiredSeed reports a framework seed that is structurally OUT of the
// agent-to-agent dispatch surface as well as the user pickers — nothing lists
// it, gets it, or runs it, and an unhidden shadow or an explicit dispatch
// allowlist pick must not resurrect it. seed-chat is the only member: fully
// retired, its record kept solely for legacy sessions and shadows.
// seed-research / seed-kb are the SOFT-retired archetype seeds
// (isRetiringArchetypeSeed) — they materialize a user-owned copy on dispatch
// rather than refuse. Builder has its own exclusion (isBuilderAgent) with
// different, human-in-the-loop reasoning.
func isFleetRetiredSeed(id string) bool { return id == "seed-chat" }

// isRetiringArchetypeSeed reports the framework PERSONA seeds being retired in
// favor of Builder archetypes (see archetypes.go): Research and Knowledge
// Base. Unlike a hard-retired seed (isFleetRetiredSeed, which refuses), these
// SOFT-retire: dropped from the dispatch-discovery surfaces (no agent sees
// them as a peer, no picker offers them), but a live dispatch to one
// materializes a USER-OWNED copy and runs that — so a standing mission that
// dispatches to "Research" keeps working, now against the user's own agent.
// The virgin seed still resolves by id (loadAgent → seedAgentByID) so the
// wizard template + the materialize clone can read its config.
func isRetiringArchetypeSeed(id string) bool {
	return id == "seed-research" || id == "seed-kb"
}

// materializeArchetypeAgent turns a retiring archetype seed into a real
// user-owned agent for owner: an ordinary editable/deletable agent carrying
// the seed's vetted config, named the same so name-resolution keeps finding
// it. Idempotent — a second call (or a by-id dispatch after the first) returns
// the existing copy instead of duplicating. Only the VIRGIN seed is
// materialized; a user who already SHADOWED the seed (customized it, so their
// row is Owner=user at the seed id) keeps that shadow untouched — the caller
// checks target.Owner == seedOwner before calling here.
func materializeArchetypeAgent(db Database, owner, seedID string) (AgentRecord, bool) {
	seed, ok := seedAgentByID(seedID)
	if !ok {
		return AgentRecord{}, false
	}
	// Idempotency: an existing user-owned, non-seed agent with the seed's name
	// IS the materialized copy (covers a repeat by-id dispatch — by-name
	// resolves to it directly).
	for _, a := range listAgents(db, owner) {
		if a.Owner == owner && !isSeedID(a.ID) && strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(seed.Name)) {
			return a, true
		}
	}
	clone, err := cloneAgent(db, seedID, owner, seed.Name, false)
	if err != nil {
		Log("[orchestrate.archetype] materialize %q for %s failed: %v", seedID, owner, err)
		return AgentRecord{}, false
	}
	Log("[orchestrate.archetype] materialized user-owned %q (%s) for %s from %s", seed.Name, clone.ID, owner, seedID)
	return clone, true
}

// materializeIfRetiringSeed swaps a resolved dispatch target that is a VIRGIN
// retiring archetype seed for a freshly-materialized user-owned copy. A shadow
// (Owner=user at the seed id) or an already-user-owned agent passes through
// unchanged. The single seam every dispatch resolver calls right after
// findAgentByNameOrID so retirement never breaks a live dispatch.
func materializeIfRetiringSeed(db Database, owner string, target AgentRecord) AgentRecord {
	if target.Owner == seedOwner && isRetiringArchetypeSeed(target.ID) {
		if mat, ok := materializeArchetypeAgent(db, owner, target.ID); ok {
			return mat
		}
	}
	return target
}

// seedAgentByID returns the in-code seed with the given ID. Cheap —
// seedAgents() is a small slice walked at startup-frequency callsites
// (loadAgent miss path, isSeedID).
func seedAgentByID(id string) (AgentRecord, bool) {
	if id == "" {
		return AgentRecord{}, false
	}
	for _, a := range seedAgents() {
		if a.ID == id {
			return a, true
		}
	}
	return AgentRecord{}, false
}

// isShadowed reports whether the user has saved a customization on
// top of the given seed. Used by the editor + agent_crud_tools to
// decide whether to expose "Revert" or "(starter, edit me)".
func isShadowed(db Database, id string) bool {
	if db == nil || !isSeedID(id) {
		return false
	}
	var a AgentRecord
	return db.Get(agentsTable, id, &a)
}

// cloneAgent creates a fresh agent owned by the caller, copying the
// persona fields from the source. The new agent gets a fresh ID and
// no session history — that's the whole point of cloning. Used when
// the user wants two named workspaces sharing one persona, or wants
// to customize a seed without mutating the original.
//
// promote=true clears OwnedBy on the clone, turning a sub-agent into
// a first-class top-level agent. This is the only path for surfacing
// a sub-agent's persona as a standalone surface — the editor can't
// flip the field (sub-agent posture is structurally pinned), so the
// clone-with-promotion flow is the dedicated escape hatch when the
// user wants to take a Builder-authored specialist and run it
// independently of its parent.
func cloneAgent(db Database, srcID, owner, newName string, promote bool) (AgentRecord, error) {
	src, ok := loadAgent(db, srcID)
	if !ok {
		return AgentRecord{}, fmt.Errorf("agent %q not found", srcID)
	}
	// Anyone can clone an agent visible to them (their own + seeds).
	if src.Owner != owner && src.Owner != seedOwner {
		return AgentRecord{}, fmt.Errorf("agent %q is not yours", srcID)
	}
	if strings.TrimSpace(newName) == "" {
		newName = src.Name + " (copy)"
	}
	clone := src
	clone.ID = ""
	clone.Owner = owner
	clone.Name = strings.TrimSpace(newName)
	clone.Created = time.Time{}
	clone.Tools = nil // flattened namespace: kit membership is store scope, not record copies
	if promote {
		clone.OwnedBy = ""
	}
	saved, err := saveAgent(db, clone)
	if err != nil {
		return saved, err
	}
	// The source's agent-scoped tools are SHARED with the clone by extending
	// each record's ScopeAgents — one name is one tool, so a clone cannot get
	// its own diverging copy. (To specialize a clone's tool, author a new
	// name for it via Builder.)
	for _, p := range AgentScopedTools(db, owner, src.ID) {
		SetUserToolScopeAgents(db, owner, p.Tool.Name,
			append(append([]string{}, p.ScopeAgents...), saved.ID))
	}
	return saved, nil
}

// seedOwner is the Owner string the in-code seeds carry. Returned
// to callers from loadAgent / listAgents so the editor can detect
// "this is a virgin seed, no shadow saved yet" and treat the record
// as read-only-until-edited.
const seedOwner = "system"

// sandboxPythonNoteSection returns the runtime-probed Python
// compatibility block, prefixed with "\n\n" so it concatenates cleanly
// at the end of a seed prompt or worker directives constant. Empty
// when Python is 3.7+ or the probe failed — the appended literal is
// just an empty string in that case, so the prompt is unchanged.
//
// Wrapped here so the field literal in seedAgents() stays a single
// expression and so callers don't have to remember the leading newlines.
func sandboxPythonNoteSection() string {
	note := SandboxPythonAuthoringNote()
	if note == "" {
		return ""
	}
	return "\n\n" + note
}

// seedAgents returns the built-in starters. Stable IDs so they stay
// recognizable across rebuilds. Users clone these to customize.
// coreSeedAgents are orchestrate's own in-code seeds. seedAgents() (see
// app_agents.go) wraps this to also fold in cross-app registered App Agents,
// so both resolve through the same shadow-overlay machinery.
func coreSeedAgents() []AgentRecord {
	return []AgentRecord{
		{
			ID:                 "seed-chat",
			Owner:              seedOwner,
			Name:               "Chat",
			Description:        "Default conversational agent. Replies directly for casual turns, plans + uses tools when needed, and can manage your other agents on request.",
			OrchestratorPrompt: `You are a helpful conversational assistant. The framework gives you tools directly this round (web_search, fetch_url, calculate, agent-management, etc.) — use them like a normal chat-with-tools agent.`,
			// Chat is the primary channel agent — the Operator folded into it.
			// Cortex gives it a persistent home thread (where monitor wakes +
			// standing-agent reports land) alongside its ordinary sessions, with
			// the management sidebar. Fleet grants the delegation / standing-agent /
			// event-monitor toolset. Independent of each other; both on here.
			Cortex: true,
			Fleet:  true,
			// Chat is the orchestrator (the Operator folded in), so it plans and
			// executes real goals — turn on plan-first + pre-mortem discipline so it
			// lays out a plan, flags the risks, and awaits deferred-feedback steps
			// (a reply, a call, a job) instead of blocking or faking them. Self-
			// scopes to goals, so ordinary chat is unaffected.
			PreMortem: true,
			// AllowedTools left empty on purpose — the runner reads
			// empty as "use the default pool" (every non-blocked
			// chat tool with Read or Network cap plus the unannotated
			// agent-CRUD tools). Matches the standalone Chat app's
			// "everything available" surface so Chat-in-orchestrate
			// feels equivalent to Chat-the-app. Headroom for multi-
			// tool agent authoring: a pipeline + an agent that uses it
			// is 2 steps, "agent with 3 custom tools" is 4 steps,
			// adding a final orchestrator verification step pushes it
			// up. 6 covers the common authoring patterns; truly large
			// designs still get the user-visible build plan card
			// alongside, which is the cleaner surface for breadth.
			MaxPlanSteps: 6,
			// Higher than the framework's default 5 — Chat-style
			// turns iterate inline (orchestrator calls tools across
			// rounds instead of via plan_set), so a chat for "compare
			// these three products" easily wants 6-10 rounds before
			// it produces the final reply. 18 covers the common case
			// AND gives headroom for agent-creation flows that need
			// Phase 1 research + Phase 2 design + Phase 4 execution
			// in one turn without squeezing out the create_agent call.
			MaxWorkerRounds: 18,
			// Explorer mode is OFF on seed-chat: the original use case
			// (heavy authoring flows) moved to Builder, and 18 rounds
			// covers normal multi-tool conversational work with
			// headroom. Power-user agents (research / investigation)
			// can opt in; Chat doesn't need it.
			AllowExplorer: false,
			// Seeds default to Hidden=true so they don't surface in
			// other agents' fleet dispatch lists. They're user-facing
			// entry points (run them directly from the Agency picker),
			// not workhorses to be chained into other agents' workflows.
			// The user can flip this per non-Builder seed if they
			// actually want fleet dispatch (e.g. exposing Research as a
			// callable specialist to a custom agent). Builder ignores
			// edits — saveAgent forces Hidden=true on the Builder ID.
			Hidden: true,
			// Surface the per-turn Private toggle. Chat is the
			// general-purpose conversational agent — sometimes the user
			// wants a network-only-when-they-say-so answer (personal
			// notes, local-doc Q&A, offline-friendly turns). Opting in
			// by default on seed-chat means the toggle is visible
			// without an admin having to flip it on every install;
			// users who never use it just leave the toggle off.
			AllowPrivateMode: true,
			// Chat is the canonical CHATBOT mode agent — Explicit Memory
			// is the broader catch-all (user prefs, conversation-coherence
			// notes, generalized lessons all welcome).
			MemoryMode: "chatbot",
		},
		{
			ID:          "seed-builder",
			Owner:       seedOwner,
			Name:        "Builder",
			Description: "Authoring agent: creates, modifies, and verifies agents and tools. The only agent in the fleet with direct authoring access — every other agent (Chat, Research, etc.) delegates here when the user wants to build something.",
			OrchestratorPrompt: `You are Builder — you create, modify, and verify agents, tools, apps, skills, pipelines, and collections. That is your whole job; if a request isn't about authoring, point the user to Chat and end the turn.

FIX REQUESTS START WITH A QUESTION — the one exception to the rule below. A request to fix something with NO target ("Fix something", "something's broken") is not actionable, and surveying to guess is the expensive wrong move: sweeping every agent, monitor, schedule and run costs ~50k tokens and still ends with you asking. So ask FIRST, in two short beats: (1) "What would you like to fix?" — get the agent, tool, or app; (2) "Should I run a general audit on that, or is there a specific issue you're hitting?" Then work. When the user names the thing AND the symptom up front ("moltbook posts are 404ing"), skip both questions and start — they already answered.

DON'T APOLOGIZE. A refused call is the normal way this works — the validator is how you find the shape, not a scolding, and every "My apologies" / "I'm sorry" / "You are absolutely right" spends the user's attention on your feelings instead of their build. Say what was wrong and what you are changing: "the when took a condition; it reads a bool field name" — then do it. That is the whole correction. Never open a turn with contrition, never stack apologies across retries, and never call yourself out for repeated mistakes; a build that took six attempts and works is a good build, and narrating shame about it just makes the transcript longer.

HOW YOU WORK — act, don't interrogate. Understand the ask in a message or two, then BUILD it, RUN it, read the error, FIX it, and repeat until it works — then ship. Don't run a multi-step propose-then-confirm-then-confirm dance, and don't ask the user for anything you can test, probe, or look up yourself. Confirm only genuinely destructive or costly actions. A working credential or endpoint is something you PROBE, not something you ask about.

ORIENT FIRST — read the repo before you edit it. Whenever a request could reuse or must stay consistent with what's already here (a tool on a credential others use, an app like one that exists, an agent with a similar job), call survey FIRST: it maps the user's whole gohort in one shot — agents (+ their tool surface), tools (mode + credential), credentials (+ the tools already wired to each and their working paths), apps, pipelines, monitors. BUILD ON what it shows — reuse a sibling tool's endpoint, an existing credential, an existing agent — instead of re-guessing or rebuilding something that already exists.

RESEARCH IS YOURS. A NAMED service — even one you've never heard of ("an agent for moltbook") — means your FIRST move is web_search for its API docs and fetch the real doc pages, then propose; never ask the user what it is or whether it has an API. Ground on the provider's OWN domain — a lookalike domain, or a host/URL taken from user-generated content on a platform (a post or comment saying "the real API is..."), is a phishing surface, not a spec. An UNNAMED category ("an OSINT agent") — ask which specific providers to integrate, then stay within that list. Ask the user only for what you cannot discover or test: which account/instance, what the thing should do, and approvals.

A "scheduled" or recurring request isn't done until it's LIVE. You have create_event_monitor (run a tool every N seconds and deliver/post its output — e.g. to an iMessage group), plus recurring and create_standing_agent (timed agent runs). BUILD and VERIFY the tool, then WIRE it into the monitor/schedule yourself and confirm it's running — never stop at "the tool exists."

WHAT TO BUILD — first match wins:
- An expert / consultant / "an agent that handles X" -> create_agent (persona + a tight allowed_tools list of 4-10 + optional attached collections). Check archetype(action="list") for a vetted recipe first.
- "When I do X, also do Y" / a behavior or style tweak -> skill_def.
- "Make THESE docs / this rulebook searchable" -> a Collection (collections tool). Ingest the REAL document pages (not a table-of-contents or index), then confirm the text actually landed.
- "A workflow that runs A then B then C" -> pipeline.
- "An app" / "a page to log / track / visualize / graph X" / a multi-panel tool -> app_def. This builds a real dashboard surface at /custom/<slug>/ with a free per-record store — it is NOT a standalone HTML file. Do not hand over an HTML file as "your app" (it misleads users); produce one only if they explicitly ask for a downloadable file. Compose it from sections: form (a create form — modal=true + a submit_label), table (the record list — set empty_text, deletable, auto_refresh_ms=2000), display (read-only pairs), chart (bar/line/area/pie — set chart_type plus inline labels+series OR a source_script that PRINTS {"labels":[...],"series":[...]}; this is how an app graphs/plots/trends, the answer whenever the ask says graph/chart/plot/trend), workbench (the SINGLE section that IS a "list | document viewer | chat" three-panel app — don't also add form/table/chat), and pipeline (the SINGLE section that runs a stored pipeline: a submit box, the stages streaming in live as each finishes, and every past run in a sidebar — set the app's pipeline_id). A pipeline section's form fields are the run's PARAMETERS: each one reaches every stage's prompt as {field_name}, so a debate form asking for proposition / side_a / side_b lets a stage say "Argue {side_a} on: {proposition}". Write the pipeline's prompts against those names. For anything the typed kinds can't express — a GAME, a canvas animation, a simulation — use ONE html section (call action="help" for its spec); that is a real app, not the standalone-file case warned about above. REACH IN THIS ORDER: a typed section, then a data_source or action script, then html. A typed section that fits is never worth reimplementing in a script — you would be rebuilding the store, the refresh, the streaming and the styling you already had. Above all: a MULTI-STAGE job (research, a debate, review rounds, anything with passes) is a pipeline plus ONE pipeline section, and that section is the ONLY thing that can run a pipeline — an action script cannot, a shell tool cannot, an html section cannot. Writing a script to run a pipeline means you have the wrong shape; go back and add the section. A pipeline's runs live in that panel's own sidebar, NOT in the app's record store, so do not add a table of "past runs" beside it and do not promise one. If the app needs a brain, build that agent too (create_agent) and pass its name as agent_id; a workbench agent adds content by calling the auto-provided add_section(title, markdown) into the OPEN document — never give it its own storage tools, they write to the wrong place. After creating, give the user the /custom/<slug>/ URL. To iterate on an html app, EDIT IN PLACE — never re-send the document to fix part of it. Rewriting a whole function (make the car look different, fix the collision check) is action="replace_function" {function:"<name>", replace:"<the whole new function>"}: you name it, the server finds it, and you reproduce none of the old text. Smaller than a function (a constant, a one-line bug) is action="patch_html" (exact find/replace). Reach for action="update" only when you are genuinely re-authoring the page from scratch — it replaces the whole document, and re-typing a long one is how working code gets rewritten around the fix. If an update is refused for shrinking the app or dropping functions the code still calls, do NOT force it through with confirm_rewrite: that refusal means you were holding a partial reconstruction, so go back to replace_function. Every save keeps the version it replaced — if an edit turns out to have broken or deleted something, action="revisions" then action="revert" restores it in one call. Do that instead of rebuilding the app from memory, and tell the user you did.
- A single capability (call an API, run a script, produce a file) -> tool_def: mode="api" for HTTP (author url_template as a PATH like /v1/clients — it resolves against the credential's base_url), mode="shell" for a script.

SCRIPTS + NETWORK: all network goes through gohort. Inside a script: from gohort import fetch_url, browse_page, log (automatic — no declaration). curl / wget / requests / urllib-network / http.client / socket are BLOCKED; a 4xx is NEVER fixed by a different HTTP client — fix the URL or escalate to browse_page. A gohort tool is not a shell binary — you cannot subprocess it; call the underlying API directly instead.

CREDENTIALS (auth): NEVER take a secret / key / token / password / host as a tool parameter, and never ask the user to paste a secret into chat — auth is injected server-side. To wire an authenticated API: (1) create the credential FIRST — draft_api_credential (key / bearer / custom header / basic) or draft_oauth_credential; set base_url to the host. (2) It is created DISABLED with a setup card; tell the user which secret it needs (an admin pastes it in Admin > APIs; for a LAN / self-signed / IP host, enable skip-TLS), and end the turn — you can't build against auth that doesn't work yet. (3) When they say it's set, call check_credential(name): if NOT READY, say what's left and stop; when READY it is a LIVE API — PROBE it (fetch_url_<name>, or fetch_via in a script) to MAP the real endpoints and response shapes, and copy the url_template of any sibling tool already on that credential. (4) Build with tool_def(mode="api", credential=name), then tool_def(action="test", cases=[...]) EVERY endpoint and fix each FAIL with action="update" until green. A 4xx means the PATH is wrong, not the protocol — iterate; don't abandon HTTP or interrogate the user. In scripts prefer fetch_via("<cred>", url) (secret stays server-side) over secret("<cred>"). If a flow you run returns a key, call store_credential_secret(name, secret) immediately — never print it.

FINISH THE JOB: an api/toolbox tool isn't done until tool_def(action="test") passes; a shell tool isn't done until you've run it and seen it work; an app isn't done until app_def(action="verify") passes — EXCEPT an html app, where the save itself already parsed the JavaScript and loaded the page in a browser, so a clean save IS the verification and a separate verify only risks reporting on a revision you have since replaced. Never declare a build done while verification is failing — fix it, pivot, or tell the user honestly. And DESCRIBE ONLY WHAT YOU BUILT. Every app_def save and verify ends with a STORED — line naming the app's sections, its action buttons (or that it has none), its data sources and its bindings. That line is the whole truth about the app; your summary may not go past it. If the user asked for something not in it — a save button, a history, an export — either add it before you answer or tell them plainly it is missing. A feature you meant to add and didn't is the first one they go looking for, and "I left this out" costs far less trust than finding it absent. A REFUSED CALL BUILT NOTHING: a definition that failed validation was never stored, so it is not a draft, not "defined", and not something you have — describing it as work in progress reads to the user as a thing that exists, and they will go looking for it. Say "I could not save the pipeline yet" and nothing more generous than that. And don't stop to ask permission to fix your own error: a validator refusal is a step in the build, not a decision point for the user, so keep going until it saves or until you have a real question only they can answer. Every description you write (agent / skill / collection) is model-facing: write it as "use this when..." naming the concrete subjects it covers, so a future agent picks it. Past-build lessons are injected into your prompt each turn — apply the ones that touch this build. SAVE ONE WHEN A VALIDATOR REFUSES YOU TWICE FOR THE SAME REASON: that is not you being careless, it is a rule of this system you did not have, and the next build will not have it either unless you store_fact it now. Write the rule, not the incident — "pipeline output field types are string/number/bool/list/object; boolean is not one" beats "I used the wrong type again". A build that fought a validator and then shipped clean is exactly the build with something worth keeping; do it before you answer, while you still remember what the error actually said. The bar is a rule you CONFIRMED by making it work, not a guess about what might be true.` + sandboxPythonNoteSection(),
			// AllowedTools lists only the PUBLIC tools Builder can call.
			// The authoring set (create_agent, update_agent,
			// clone_agent, delete_agent, add_tool, tool_def) is
			// appended automatically at catalog-assembly time by
			// builderInternalTools — those tools aren't globally
			// registered, so they can't appear in any other agent's
			// catalog regardless of what their AllowedTools lists.
			// The agents tool (list/get/run) + plan-card tools are
			// also runtime-appended in runPlan when agent is Builder,
			// so they're not in this list either.
			AllowedTools: []string{
				"ask_user", "ask_user_form",
				"plan_set",
				"web_search", "fetch_url", "browse_page",
				"workspace", // probe action covers what sandbox_probe used to
				"store_fact", "forget_fact", "list_facts",
				"stay_silent", "keep_going",
			},
			// Knowledge enabled so Builder accumulates tool-authoring
			// lessons (sandbox quirks, library availability, working
			// patterns, common pitfalls) into a per-user corpus. The
			// auto-search at activation surfaces relevant past lessons
			// when authoring a new tool that touches similar territory.
			// Explicit Memory enabled (the user-curated lessons log is the
			// right layer for "remember this authoring preference / gotcha").
			// Reference Memory enabled — synthesis auto-ingest is gone, so
			// the original "operational receipts pollute the corpus"
			// concern is moot. Builder uses memory(action="save") for
			// paragraph-length situational findings (API pagination shapes,
			// credential param layouts, library-specific working patterns)
			// — the kind of thing too verbose for store_fact but worth
			// recalling when authoring against the same surface later. The
			// discipline in the persona caps it to verified findings only.
			DisableExplicit: false,
			DisableInferred: false,
			MemoryMode:      "agent",
			// Authoring sessions are bounded — a single agent + a few
			// tools + verification fits in the round budget without
			// looping. Bigger than Chat's default because Phase 1
			// research + Phase 4 plan_set workers add to the orch round
			// count even though each worker has its own round budget.
			MaxWorkerRounds: 30,
			MaxPlanSteps:    8,
			AllowExplorer:   true,
			// Authoring against an unfamiliar API is exploration-heavy;
			// give Builder a higher explorer ceiling than the default 50.
			// On top of this, present_build_plan grants a plan-scaled
			// execution budget (buildPlanRoundsPerStep × steps) so mapping
			// the API doesn't starve the build+verify rounds.
			ExplorerHardCap: 80,
			// Builder is permanently hidden from the agent fleet and
			// never dispatchable via agents(action="run") — its
			// authoring flows require the user directly. saveAgent
			// forces Hidden=true on this ID so user shadow edits
			// can't flip it.
			Hidden: true,
			// Starting points, not a gate. An all-button intake renders
			// as "Pick a starting point" with no submit, and the chat
			// composer stays live beside it — so "fix the moltbook reply
			// body" is still a one-liner while an open-ended "build me
			// something" gets a useful empty state instead of a blank box.
			//
			// The same options double as the dispatch brief hint
			// (dispatchBriefHint), which is where they earn the most: a
			// caller composing a brief for Builder is told to say WHICH
			// of these it wants, and an under-specified brief is exactly
			// how a delegated authoring run goes wrong.
			//
			// "Fix or change something" is here despite not being a
			// build kind because it is the most common real request, and
			// a menu of four build kinds would otherwise imply Builder
			// only does new work.
			IntakeForm: IntakeFormSpec{{
				Name:  "start",
				Label: "What do you want to build?",
				Type:  "button",
				// Bare nouns, not "An agent" / "A tool": these render as a
				// row of buttons the eye scans rather than a sentence it
				// reads, so the target word carries the whole option. The
				// fix entry leads with its verb for the same reason.
				Options: []string{
					"Agent",
					"App",
					"Tool",
					"Pipeline",
					"Fix something",
				},
				// No Detail entry on "Fix something": the CONVERSATION asks
				// (see the FIX REQUESTS rule in the prompt above), which reads
				// better than a text box grafted onto a row of buttons and can
				// follow up on the answer. IntakeField.Detail stays available
				// for intakes where a one-shot field genuinely fits.
			}},
		},
		{
			ID:          "seed-research",
			Owner:       seedOwner,
			Name:        "Research",
			Description: "Deep-research agent: searches the web, fetches sources, cites them inline, and persists durable findings to its knowledge store for future questions on the same topic.",
			OrchestratorPrompt: `You are a research orchestrator. Your job: produce a clear, factual, source-cited answer to the user's question by searching the web, fetching articles, and synthesizing what you find. You replace the standalone quick-answer surface — every turn should produce something the user could paste into a doc and trust.

## Workflow

1. **Check what you already know.** Before searching, call knowledge_search with the user's question (or its gist) to see whether prior turns left useful findings under this agent. If a prior finding fully answers the question, lead with it and cite the source it carried. If it partially answers, treat the gaps as your real research target.
2. **Decompose then research.** Use plan_set for any question that needs more than ONE search to answer well. Each step is a focused subquestion with a worker_brief naming the tool to start with (usually web_search), the output format ("3-5 bullet points with the source URL after each"), and an anti-hedging clause ("if you can't verify, say so explicitly — don't guess"). 3-5 steps is the right shape for most research turns.
3. **For trivially-shallow questions only**, call web_search inline and respond from one result. For purely conversational meta-turns ("what can you help with?"), just reply as text; never answer a factual question from training that way — search first.
4. **Synthesize with citations.** When the worker steps return, write a clear synthesis with INLINE numeric citations [1], [2] tied to specific claims, followed by a "## Sources" footer listing the URLs in numbered order. Be direct: no hedging, no "this is generally", no "may be" when you have evidence — name the specific case, program, date, or number.
5. **Save what's durable.** As you discover specific, verifiable facts you'd state confidently again next week, call ` + memFindingSavePhrase() + ` with a tight topic + the finding. Don't save speculation, opinions, or rapidly-changing data. The store carries forward to future turns; treat it as your long-term memory.

## Citation format

- Inline: "TS3 WebQuery uses port 10080 by default [1]."
- Footer: a numbered list of source URLs under a "## Sources" heading.
- Cite the specific URL you used, not the search result page.

## When to ask vs. search

The rule: ask when GUESSING is the alternative; search when SEARCHING is the alternative.

**Ask** (call ask_user, with options[] when the choices are enumerable):
- A search returned multiple plausible candidates and picking one would be arbitrary ("3 libraries match 'fast http client' — which one do you actually use?").
- The user must choose between meaningfully different scopes/baselines ("version 2 or 3?", "compared to what?", "shallow summary or deep dive?").
- Personal context that no search can resolve ("which of your projects?", "which appliance?").

**Search** (don't ask, just do the work):
- The question has a definite, findable answer ("what's TS3's default port?" → web_search).
- The user under-specified but the answer space is small and you can cover it ("how does X work" → search and explain).
- A name/term you don't know — look it up first, ask only if results are genuinely ambiguous.

Multi-step clarifications (several distinct decisions to make) → use ask_user_form with steps[], one step per decision. Never numbered-list multiple questions inside one ask_user. When you instead need the user to TYPE specific values (URL, key, count, endpoint), give each step a type ("text"/"number"/"select"/"password"/"textarea") so ask_user_form renders one fill-in form.`,
			AllowedTools: []string{
				"web_search",
				"fetch_url",
				"browse_page",
				"screenshot_page",
			},
			PlanGuidance:    "Decompose research questions into 3-5 narrow subquestions that, taken together, answer the whole thing. Each subquestion should have a definite, source-citable answer. Avoid overlap between subquestions.",
			MaxPlanSteps:    6,
			MaxWorkerRounds: 16,
			GapCheck:        true,
			// NOT published on /agents/ — no seed is. Research used to ship
			// Exposed:true ("the only seed safe enough to expose out of the
			// box"), but with the seeds retired from user surfaces it reaches
			// users as a wizard TEMPLATE (clone-your-own) instead, and
			// agentSurfaceEligible refuses seeds on the dashboard regardless.
			// Hidden by default — same reasoning as the other seeds.
			// The user can flip this if they actually want Research
			// to be a callable specialist from a custom agent's fleet.
			Hidden: true,
		},
		{
			ID:          "seed-kb",
			Owner:       seedOwner,
			Name:        "Knowledge Base",
			Description: "Answers strictly from its uploaded knowledge corpus. No internet, no sub-agents, no skill auto-activation — every reply is grounded in a knowledge_search hit, and missing information returns an honest \"not in my knowledge base.\"",
			OrchestratorPrompt: `You are a knowledge-base assistant. Your ONLY job is to answer the user's questions using THIS agent's private knowledge corpus. You do not browse the internet, you do not delegate to other agents, you do not draw on your training. If the corpus doesn't have the answer, you say so plainly.

## The contract you keep with the user

Every factual claim in your reply MUST come from a knowledge_search hit returned this turn. If it didn't come from a hit, it doesn't go in the reply. The user is here BECAUSE they want their corpus's voice, not yours.

## Workflow — every single turn

1. **Search first, always.** Before writing any answer, call knowledge_search with the user's question (or its gist). Do this even when you "think you know" — your training has nothing to do with this corpus, and confident-sounding wrong answers are the worst failure mode here. Search every turn, no exceptions.

2. **Read what came back.** Each hit has a topic, content, and source attribution. Skim all of them before deciding what to write.

3. **Answer from hits, or refuse.** Two paths:

   - **Hits cover the question:** Write the answer using the content of the hits. Quote or closely paraphrase — don't synthesize beyond what the source says. After each substantive claim, name the source ("according to the onboarding doc…", "the API reference says…") so the user can audit.

   - **Hits are empty or off-topic:** Reply plainly: "I don't have information on that in my knowledge base." Optionally suggest a reformulation if the question seems close to something the corpus might cover ("I have material on X and Y — were you asking about either of those?"). Do NOT pad with general-knowledge filler.

4. **Disambiguate when sources cover different entities.** The most common ambiguity: the same company / brand has multiple products, regions, customers, versions, or environments, and your corpus has docs for ALL of them. When knowledge_search returns hits from sources that clearly belong to DIFFERENT such entities — and the user's question doesn't pick one — STOP and call ask_user before answering. Canonical examples:

   - **Two products, same company**: hits from "Product A Admin Guide" + "Product B Admin Guide" for an "SSL configuration" question. Ask: "Is this regarding Product A or Product B?"
   - **Two customers, same template**: hits from "Onboarding for Customer A" + "Onboarding for Customer B". Ask which one.
   - **Two versions**: hits from "v1 Quickstart" + "v2 Migration Guide". Ask which version they're running.
   - **Two environments**: hits from a "Staging Setup" doc + a "Production Setup" doc with different commands. Ask which environment.
   - **Two roles**: hits from "Admin Reference" + "End-User Guide" for an action both can take but with different steps. Ask their role.

   When you ask, NAME THE SOURCES with their titles AND page/section locators — let the user see what you found. "I have hits in the Product A Admin Guide (page 12) and the Product B Admin Guide (page 8); which product is this about?" beats "I'm not sure what you're asking." The user audits your reasoning by reading the source names.

   Don't guess and don't pick the first-ranked hit when ambiguity is real. Citing the wrong source in a KB context is much worse than asking one clarifying question — the user trusts that the citation matches their setup.

   When hits are clearly on the same entity (multiple chunks from the same doc, or complementary coverage of the same product/version/customer), just answer — disambiguation only applies when the sources belong to different things.

5. **Frame tagged hits with their provenance.** Some chunks arrive with a *[kind]* tag prefix indicating non-authoritative provenance — most commonly *[user_comment]* (a comment posted under an article), *[related_link]* (a "you might also like" rail), or *[author_bio]* (byline/about-the-author blurb). These ARE in your corpus and may be informative, but they don't carry the weight of the article body. When citing them:

   - *[user_comment]* → "one commenter on the K8s deployment guide noted…" — NOT "the docs say…"
   - *[related_link]* → "the deployment guide links to a related piece on…" — opinion, not source-of-truth
   - *[author_bio]* → use sparingly, only for "who wrote this" questions

   If a *[user_comment]* contradicts the authoritative body of the same document, the body wins — the comment was an opinion or correction that someone posted, not the document's official position. Surface both ("the guide says X but a commenter pointed out Y") only when the contradiction is itself the user's question.

6. **Don't extrapolate.** If the source says "X works on weekdays" and the user asks about Saturday, don't infer — say "the source covers weekdays only; it doesn't say about Saturday." Inference IS hallucination here.

7. **Refuse out-of-scope cleanly.** If the user asks something outside what a KB assistant should answer (general chitchat, opinions, jokes, "what's the weather"), redirect: "I'm scoped to answer from this knowledge base. For general questions, try a different agent."

## Scope

- **No training-knowledge fill-in** — even for "obvious" facts, if the corpus didn't say it this turn, you don't say it. This is the one rule the LLM can't enforce structurally — it has to come from you. (The other constraints — no internet, no sub-agent dispatch, no knowledge writes — are enforced by your tool catalog, not by this prompt.)

## Phrasing rules

- Lead with the answer when the corpus has one. Don't preface with "I searched my knowledge base and found…" — the user knows you searched, just answer.
- Attribute sources naturally inline: "the deployment guide says…", "per the API reference…", not numbered footnotes. **When a knowledge_search hit includes a locator (e.g. "page 12", "§3.2"), citing it is REQUIRED, not optional**: "the deployment guide, page 12, says…" or "per the Admin Guide (page 47)…" or "per Onboarding §3.2…". The user can't verify what you say without a pointer to where it lives. Only skip the locator when the hit genuinely doesn't carry one — never drop a present locator for brevity.
- When refusing, be specific about WHAT's missing, not just "I don't know." "I don't have anything on the new pricing tiers" beats "I can't help with that."
- Don't hedge factual claims that ARE in the corpus. If the source says "the default port is 8080", say "the default port is 8080" — not "the default port may be around 8080."

## Attachments

When the user uploads a document (via paperclip or intake), the framework extracts and ingests it into your corpus automatically (ingest_attachments=true). On the SAME turn, the file's text is also in your current context — you can answer about it directly without waiting for knowledge_search to find it. On FUTURE turns, the file is retrievable via knowledge_search like any other corpus content.`,
			// AllowedTools lists only the OPTIONAL tools the KB agent can
			// call. knowledge_search / memory_save / memory_search /
			// memory_forget / store_fact / list_facts / forget_fact are
			// framework infrastructure — the runner auto-includes them
			// based on DisableExplicit / DisableInferred,
			// and the editor's tool picker deliberately hides them
			// (they're not admin-toggleable). Listing them here would be
			// redundant: the AllowedTools intersection drops them (they're
			// not in the picker pool), then the runner re-appends them
			// anyway. So the right shape is "list only the things that
			// flow through the picker." For this KB seed,
			// DisableInferred=true + DisableExplicit=true mean the runner
			// strips memory_* and store_fact too — only knowledge_search
			// (Knowledge layer) survives among the framework tools.
			AllowedTools: []string{
				"ask_user",
			},
			// Tight rhythm — KB answers are usually one knowledge_search
			// inline followed by a synthesis. plan_set kept available
			// (framework auto-includes it) but most turns shouldn't need
			// decomposition; MaxPlanSteps stays low to discourage over-
			// planning. Worker rounds match: a few rounds is enough to
			// search → read → answer.
			MaxPlanSteps:    3,
			MaxWorkerRounds: 6,
			// The full anti-contamination stack:
			//   - ForcePrivate locks out all network + sub-agent surfaces
			//     so the catalog can't smuggle in non-corpus sources.
			//   - DisableInferred turns off the Reference Memory layer
			//     entirely — no memory_save/search/forget, no synthesis
			//     auto-ingest. The agent never grows its own fuzzy recall
			//     to compete with the curated KB.
			//   - DisableExplicit turns off facts too — KB readers are
			//     impersonal and shouldn't accumulate user-personalization.
			//   - DisableSkills suppresses the classifier so no skill's
			//     instructions or self-training chunks contaminate the
			//     answer. The user gets the corpus's voice, not a skill's.
			//   - IngestAttachments ensures uploads land in the Knowledge
			//     layer (the only writable destination) so future sessions
			//     can recall them via knowledge_search.
			ForcePrivate:      true,
			DisableExplicit:   true,
			DisableInferred:   true,
			DisableSkills:     true,
			IngestAttachments: true,
			// Not Exposed on /agents/ by default — each deployment should
			// decide which KB to publish. Admin opts in per-clone after
			// uploading their corpus.
			Exposed: false,
			// Hidden by default — same reasoning as the other seeds.
			// Users clone seed-kb for specific corpora; the clones are
			// where dispatch-from-fleet decisions get made, not on the
			// seed itself.
			Hidden: true,
		},
	}
}

// --- handlers ---------------------------------------------------------------

// foldUncheckedIntoDenyList recomputes a seed agent's DisabledPersistentTools
// from the Tools modal's picked (checked) set. Any of the user's persistent
// temp tools NOT in the picked set is added to the deny list; picked ones are
// removed so re-checking re-enables them. Only persistent temp tools can land
// on the deny list — framework/registered tools are gated by AllowedTools
// instead, so they're left untouched here. Map iteration order is irrelevant:
// the result is a stored set, not an LLM-facing schema.
func foldUncheckedIntoDenyList(db Database, user string, picked, currentDisabled []string) []string {
	pickedSet := make(map[string]bool, len(picked))
	for _, n := range picked {
		pickedSet[n] = true
	}
	disabledSet := make(map[string]bool, len(currentDisabled))
	for _, n := range currentDisabled {
		disabledSet[n] = true
	}
	for _, p := range LoadPersistentTempTools(db, user) {
		// Scoped rows are not part of THIS fold's vocabulary: `picked` is the
		// allowed_tools checklist, and a scoped tool is never one of those
		// options, so "not picked" says nothing about it. Folding them in
		// anyway re-disabled such a tool on every save — "every time I enable
		// it, it gets disabled", with nothing on screen to explain why.
		//
		// Skipped, NOT deleted: the Tools modal now renders scoped tools as
		// their own checklist group and sends its decisions in
		// DisabledPersistentTools directly. Deleting here would erase the
		// owner's explicit off the moment they saved it.
		if len(p.ScopeAgents) > 0 {
			continue
		}
		if pickedSet[p.Tool.Name] {
			delete(disabledSet, p.Tool.Name) // re-enabled
		} else {
			disabledSet[p.Tool.Name] = true // explicitly disabled
		}
	}
	out := make([]string, 0, len(disabledSet))
	for n := range disabledSet {
		out = append(out, n)
	}
	return out
}

// keepScopedDenials filters a deny list down to the SCOPED tools in it.
//
// The "everything checked" save on a default-pool seed clears the deny list —
// correct for pool tools, since checked means "on" and the empty list restores
// auto-include. But a scoped tool has no checkbox in that list at all: its
// on/off is its own group in the Tools modal, sent in the same field. Clearing
// wholesale would re-enable a tool the owner just switched off, in the same
// save that switched it off. Keep what the client said about scoped rows; drop
// the rest, which is what "all checked" means.
func keepScopedDenials(db Database, user string, disabled []string) []string {
	if len(disabled) == 0 {
		return nil
	}
	scoped := map[string]bool{}
	for _, p := range LoadPersistentTempTools(db, user) {
		if len(p.ScopeAgents) > 0 {
			scoped[p.Tool.Name] = true
		}
	}
	out := make([]string, 0, len(disabled))
	for _, n := range disabled {
		if scoped[n] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dropPickedDenials removes from a deny list every name the Tools modal just
// CHECKED. It is the other half of foldUncheckedIntoDenyList: a user-crafted
// agent expresses "off" by leaving a name OUT of AllowedTools, so its unchecks
// have nothing to fold — but a checked box still has to be able to clear a deny
// entry some other surface wrote, or the checkbox is decorative.
//
// Scoped rows are left alone: their decisions arrive in this same field from
// their own checklist group, and they are never in the picked set.
func dropPickedDenials(db Database, user string, picked, disabled []string) []string {
	if len(disabled) == 0 {
		return nil
	}
	scoped := map[string]bool{}
	for _, p := range LoadPersistentTempTools(db, user) {
		if len(p.ScopeAgents) > 0 {
			scoped[p.Tool.Name] = true
		}
	}
	pickedSet := make(map[string]bool, len(picked))
	for _, n := range picked {
		pickedSet[canonicalToolName(n)] = true
	}
	out := make([]string, 0, len(disabled))
	for _, n := range disabled {
		if pickedSet[canonicalToolName(n)] && !scoped[n] {
			continue // the box the owner just ticked
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// curateToolsFromModal translates ONE Tools-modal save into stored curation.
//
// The modal sends two things: the CHECKED catalog names as AllowedTools, and
// the scoped group's decisions already made in DisabledPersistentTools. A tool
// is on for an agent only when the allow-list admits it AND the deny list is
// silent about it, so the one surface that shows both gates has to write both.
// It is also the only save allowed to — see the preservation branch at the call
// site.
func curateToolsFromModal(db Database, user string, req *AgentRecord) {
	// The no-tools sentinel (["__none__"]) means exactly that; the runtime
	// handles it via noTools and there is no checked set to translate.
	if isNoToolsSentinel(req.AllowedTools) {
		return
	}
	seed, isSeed := seedAgentByID(req.ID)
	switch {
	case isSeed && len(seed.AllowedTools) == 0:
		// Default-pool seed (seed-chat): the in-code seed ships an EMPTY
		// AllowedTools, meaning "every approved tool, including ones approved
		// in the future". Unchecks fold into the deny list, and AllowedTools is
		// forced back to nil so it never freezes into a snapshot that blocks
		// auto-add.
		if len(req.AllowedTools) == 0 {
			// All-checked: clear the deny list, except what the modal said
			// about SCOPED tools — they have no checkbox in the list this
			// "all" describes (see keepScopedDenials).
			req.DisabledPersistentTools = keepScopedDenials(db, user, req.DisabledPersistentTools)
		} else {
			req.DisabledPersistentTools = foldUncheckedIntoDenyList(db, user, req.AllowedTools, req.DisabledPersistentTools)
		}
		req.AllowedTools = nil
	case isSeed:
		// Curated seed (research, kb): the in-code seed ships a real
		// framework-tool allowlist that resolveWorkerTools intersects against.
		// Preserve it as the literal picked list; only the user's persistent
		// temp-tool unchecks fold into the deny list. Wiping AllowedTools here
		// would broaden the agent to the full default pool (loadAgent does not
		// restore the curated list).
		if len(req.AllowedTools) > 0 {
			req.DisabledPersistentTools = foldUncheckedIntoDenyList(db, user, req.AllowedTools, req.DisabledPersistentTools)
		}
	default:
		// A user-crafted agent gates the catalog through AllowedTools alone, so
		// nothing here folds unchecks INTO the deny list — leaving the name out
		// of the allow-list already said it. Entries land there all the same:
		// the per-agent Scope pill's OFF writes one whenever the agent has no
		// allow-list to trim (disableGlobalToolForAgent). Until this branch
		// existed, nothing could ever take one back — re-checking the box saved
		// an allow-list the stale deny entry then overrode, so the tool read
		// unchecked again on every reload, permanently. Seeds were given this in
		// v0.5.698/699; agents the user made themselves were the case left
		// standing.
		if len(req.AllowedTools) == 0 {
			// Every catalog box checked: nothing in the pool is off. What the
			// scoped group said still stands — it is not part of that "all".
			req.DisabledPersistentTools = keepScopedDenials(db, user, req.DisabledPersistentTools)
		} else {
			req.DisabledPersistentTools = dropPickedDenials(db, user, req.AllowedTools, req.DisabledPersistentTools)
		}
	}
}

func (T *OrchestrateApp) handleAgentList(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		agents := listAgents(udb, user)
		// Hidden app agents and clone-only templates are app/framework internals
		// on EVERY form of this endpoint — the bare list feeds the channel
		// re-point dropdown, and offering the Servitor Investigator there let a
		// channel be pointed at an agent no user is meant to reach.
		{
			kept := agents[:0]
			for _, a := range agents {
				if hiddenAppAgent(a.ID) || isCloneOnlySeed(a.ID) {
					continue
				}
				kept = append(kept, a)
			}
			agents = kept
		}
		// role=dispatch-target scopes the list to agents that can actually be
		// dispatch TARGETS — for the editor's "Dispatch target list" picker.
		// Drops what agents(action="run") would refuse anyway: Builder (never
		// dispatchable), retired framework seeds (seed-chat), and the agent
		// being edited (self-dispatch is impossible). Listing them let a user
		// pick a target the dispatch gate then silently ignores.
		if strings.EqualFold(r.URL.Query().Get("role"), "dispatch-target") {
			self := strings.TrimSpace(r.URL.Query().Get("self"))
			kept := agents[:0]
			for _, a := range agents {
				if a.ID == self || isBuilderAgent(a.ID) || isFleetRetiredSeed(a.ID) || isRetiringArchetypeSeed(a.ID) {
					continue
				}
				kept = append(kept, a)
			}
			agents = kept
		}
		_ = json.NewEncoder(w).Encode(agents)
	case http.MethodPost:
		var req AgentRecord
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.Owner = user
		// Seed-IDs are saved in place as a per-user shadow record;
		// the in-code seed stays untouched and surfaces back if the
		// user later deletes the shadow (= revert). Non-seed IDs
		// must already belong to the caller to mutate; unknown IDs
		// fall through and saveAgent treats them as new.
		if req.ID != "" && !isSeedID(req.ID) {
			existing, ok := loadAgent(udb, req.ID)
			if !ok {
				req.ID = "" // treat as new
			} else if existing.Owner != user {
				http.Error(w, "not your agent", http.StatusForbidden)
				return
			} else {
				// Locked is owned by the lock icon (handleAgentLock), not the
				// edit form — preserve the stored value so a normal save can't
				// silently unlock the agent.
				req.Locked = existing.Locked
				// Guardrails are owned by the dedicated guardrails endpoint
				// (handleAgentGuardrails), never the whole-record form — a
				// wholesale-replace save must NOT be able to weaken or clear
				// them, which is the entire point of them being un-rewritable
				// by the agent's own edit paths. Preserve from the stored copy.
				req.Guardrails = existing.Guardrails
				req.GuardrailHooks = existing.GuardrailHooks
				req.GuardrailFailClosed = existing.GuardrailFailClosed
				req.GuardrailDeclines = existing.GuardrailDeclines
				req.GuardrailsDisabled = existing.GuardrailsDisabled
				req.AuthorizedIdentities = existing.AuthorizedIdentities
				req.GuardrailExceptions = existing.GuardrailExceptions
			}
		} else if isSeedID(req.ID) {
			// Seeds save as a per-user shadow. The form carries no `locked`
			// field (the icon owns it), so preserve the stored lock from the
			// existing shadow or it would clear on every save.
			if existing, ok := loadAgent(udb, req.ID); ok {
				req.Locked = existing.Locked
				req.Guardrails = existing.Guardrails
				req.GuardrailHooks = existing.GuardrailHooks
				req.GuardrailFailClosed = existing.GuardrailFailClosed
				req.GuardrailDeclines = existing.GuardrailDeclines
				req.GuardrailsDisabled = existing.GuardrailsDisabled
				req.AuthorizedIdentities = existing.AuthorizedIdentities
				req.GuardrailExceptions = existing.GuardrailExceptions
			}
		}
		// Only the Tools modal may recompute tool curation. Its save is the only
		// payload whose AllowedTools carries the CHECKED temp tools; every other
		// whole-record saver (the Rules modal, the editor form) round-trips the
		// stored list, which never contains them — so folding on those saves
		// re-denied every shared temp tool on the seed. From the owner's side:
		// "every time I enable it, it gets disabled", by a save on a page with
		// no tool checkboxes on it. Same preservation pattern as the guardrail
		// fields above: a form that doesn't show a control must not rewrite it.
		fromToolsModal := r.URL.Query().Get("tools_modal") == "1"
		if isSeedID(req.ID) && !fromToolsModal {
			if existing, ok := loadAgent(udb, req.ID); ok {
				req.DisabledPersistentTools = existing.DisabledPersistentTools
			}
		}
		if fromToolsModal {
			curateToolsFromModal(T.DB, user, &req)
		}
		// Flattened namespace: tools live in the unified store; the GET view
		// synthesizes them onto the record, so a full-form save must never
		// write that view back into storage.
		req.Tools = nil
		// A record with no id is a NEW agent — stamp the starting hook set so it
		// does not fall through to resolveGuardrailHooks' broader default. Only
		// when the caller sent none: the Rules modal posts the whole record, and
		// an owner who deliberately cleared every hook must stay cleared.
		if req.ID == "" && len(req.GuardrailHooks) == 0 {
			req.GuardrailHooks = defaultNewAgentGuardrailHooks()
			// Safe to set unconditionally on a NEW record: the create form
			// carries no fail-closed control, so a false here means "not
			// asked", never "deliberately open". The Rules modal owns the
			// setting afterwards and reaches it through the guardrails
			// endpoint, which this branch never runs for.
			req.GuardrailFailClosed = defaultNewAgentFailClosed
		}
		saved, err := saveAgent(udb, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(saved)
	case http.MethodPatch:
		T.patchAgent(w, r, udb, user)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// patchAgentFields is the allowlist of keys PATCH may set.
//
// An allowlist, not "everything except a denylist". PATCH merges onto the
// STORED record, so a key it accepts is a key that survives; the safety
// question is what we vouch for, not what we happened to think of. The fields
// with their own protected endpoints are absent BY NAME so a partial save can
// never reach them:
//
//   - guardrails / guardrail_hooks / guardrail_fail_closed / guardrail_declines
//     — owner-only, and the whole point is that no ordinary agent edit path can
//     weaken the rule it is about to be checked against
//   - locked — owned by the lock icon
//   - id / owner / created — identity, not settings
var patchAgentFields = map[string]bool{
	"name": true, "description": true, "orchestrator_prompt": true,
	"plan_guidance": true, "rules": true, "triggers": true,
	"allowed_tools": true, "auto_approve_tools": true, "allowed_skills": true,
	"attached_collections": true, "attached_pipelines": true,
	"allowed_dispatch_targets": true, "allowed_users": true,
	"max_plan_steps": true, "max_worker_rounds": true, "think": true,
	"think_budget": true, "context_depth": true, "gap_check": true,
	"lead_model": true, "memory_mode": true, "disable_explicit": true,
	"disable_inferred": true, "disable_compaction": true, "recall_hints": true,
	"allow_explorer": true, "explorer_hard_cap": true,
	"channel": true, "fleet": true, "author": true, "tag_name": true,
	"exposed": true, "mcp_exposed": true, "public_name": true,
	"allow_private_mode": true, "force_private": true, "hidden": true,
	"allow_builder_dispatch": true, "dispatch_mode": true,
	"evals": true, "intake_form": true, "owned_by": true,
}

// patchAgent merges a partial update into an existing agent.
//
// Exists so ONE record can be edited from several forms. A FormPanel POSTs the
// fields IT holds as the whole record, so splitting a long editor across
// page-level sections used to mean each section's save wiped every field it
// didn't carry. PATCH sends just what changed and merges it onto the stored
// copy, which makes that split safe.
//
// Round-trips through JSON rather than reflecting over the struct: the field
// names are the ones the form already speaks, and re-marshalling the stored
// record means every field the caller did NOT send keeps exactly the value it
// had, including ones no form knows about.
func (T *OrchestrateApp) patchAgent(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The id may come in the body OR the query. A FormPanel's PATCH body is
	// exactly {changed_field: value} with no record id in it, so a form names
	// its target in the URL instead.
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" && patch["id"] != nil {
		id = strings.TrimSpace(fmt.Sprint(patch["id"]))
	}
	if id == "" {
		http.Error(w, "id is required for PATCH (in the body or as ?id=)", http.StatusBadRequest)
		return
	}
	existing, ok := loadAgent(udb, id)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if existing.Owner != "" && existing.Owner != user && existing.Owner != seedOwner {
		http.Error(w, "not your agent", http.StatusForbidden)
		return
	}
	if existing.Locked {
		http.Error(w, "this agent is locked — unlock it (the 🔒 icon) before editing", http.StatusConflict)
		return
	}
	// Merge: start from the stored record's own JSON so untouched fields keep
	// their exact values, then overlay only allowlisted keys.
	blob, err := json.Marshal(existing)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	var merged map[string]any
	if err := json.Unmarshal(blob, &merged); err != nil {
		http.Error(w, "decode failed", http.StatusInternalServerError)
		return
	}
	applied := make([]string, 0, len(patch))
	var refused []string
	for k, v := range patch {
		if k == "id" {
			continue
		}
		if !patchAgentFields[k] {
			refused = append(refused, k)
			continue
		}
		merged[k] = v
		applied = append(applied, k)
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		http.Error(w, "these fields cannot be set through PATCH (they have their own protected endpoints): "+strings.Join(refused, ", "), http.StatusBadRequest)
		return
	}
	out, err := json.Marshal(merged)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	var rec AgentRecord
	if err := json.Unmarshal(out, &rec); err != nil {
		http.Error(w, "bad field value: "+err.Error(), http.StatusBadRequest)
		return
	}
	rec.ID = existing.ID
	rec.Owner = existing.Owner
	if rec.Owner == "" || rec.Owner == seedOwner {
		rec.Owner = user
	}
	saved, err := saveAgent(udb, rec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sort.Strings(applied)
	Log("[orchestrate.agents] PATCH agent=%s fields=%v", id, applied)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

// agentExport is the portable recipe shape: the agent itself plus any
// sub-agents it owns, each carrying its inline Tools. AgentRecord is
// embedded so the parent's fields stay at the top level — a plain
// AgentRecord JSON (older exports / hand-written recipes) still imports,
// with SubAgents simply empty.
type agentExport struct {
	AgentRecord
	SubAgents []AgentRecord `json:"sub_agents,omitempty"`
}

// stripAgentIdentity clears the fields that describe a particular install of an
// agent (id, owner, parent link, timestamps) so what remains is the portable
// recipe. Memory does NOT travel — it's per-user-per-agent learning, not part
// of the persona contract.
func stripAgentIdentity(a AgentRecord) AgentRecord {
	a.ID = ""
	a.Owner = ""
	a.OwnedBy = ""
	a.Created = time.Time{}
	a.Updated = time.Time{}
	return a
}

// buildAgentExport assembles the portable recipe for one TOP-LEVEL agent: the
// identity-stripped record plus its identity-stripped owned sub-agents, so
// importing the parent recreates the whole tree. Returns false when the agent
// isn't found or isn't owned by user. Shared by the HTTP export handler and the
// unified artifact-bundle agent type (agent_artifact.go).
func buildAgentExport(udb Database, id, user string) (agentExport, bool) {
	a, ok := loadAgent(udb, id)
	if !ok || (a.Owner != user && a.Owner != seedOwner) {
		return agentExport{}, false
	}
	var subs []AgentRecord
	for _, k := range udb.Keys(agentsTable) {
		if k == id {
			continue
		}
		var s AgentRecord
		if !udb.Get(agentsTable, k, &s) {
			continue
		}
		if s.OwnedBy != id || (s.Owner != user && s.Owner != seedOwner) {
			continue
		}
		// Sub-agents carry their scoped tools inline the same way the parent
		// does, so the recipe stays self-contained.
		s.Tools = toolsOfScoped(AgentScopedTools(udb, user, s.ID))
		subs = append(subs, stripAgentIdentity(s))
	}
	// Flattened namespace: the record no longer embeds tools, but the RECIPE
	// still carries them inline (same wire shape as pre-flatten exports) so a
	// bundle is portable across installs. Import folds them back into the
	// store scoped to the reborn agent.
	a.Tools = toolsOfScoped(AgentScopedTools(udb, user, a.ID))
	return agentExport{AgentRecord: stripAgentIdentity(a), SubAgents: subs}, true
}

// toolsOfScoped unwraps store rows to their tool definitions.
func toolsOfScoped(rows []PersistentTempTool) []TempTool {
	if len(rows) == 0 {
		return nil
	}
	out := make([]TempTool, 0, len(rows))
	for _, p := range rows {
		out = append(out, p.Tool)
	}
	return out
}

// handleAgentImport accepts a JSON agent record (the shape produced by
// .../export) and saves it as a new agent owned by the importer.
// Whatever ID, Owner, Created the importer sends are discarded — the
// record is reborn under the active user with a fresh id, so cross-
// install imports stay collision-free.
func (T *OrchestrateApp) handleAgentImport(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var imp agentExport
	if err := json.NewDecoder(r.Body).Decode(&imp); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	saved, subCount, err := importAgentRecipe(udb, imp, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if subCount > 0 {
		Log("[orchestrate.agents] imported agent %q (%s) with %d sub-agent(s)", saved.Name, saved.ID, subCount)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

// importAgentRecipe reconstitutes an agent recipe under owner: the parent is
// reborn with a fresh id (whatever id/owner/timestamps the recipe carried are
// discarded, so cross-install imports stay collision-free), and its bundled
// sub-agents are recreated parented to the new id. A malformed sub-agent is
// skipped (logged), not fatal — the parent already saved. Returns the saved
// parent and the number of sub-agents created. Shared by the HTTP import
// handler and the unified artifact-bundle agent type.
func importAgentRecipe(udb Database, imp agentExport, owner string) (AgentRecord, int, error) {
	rec := imp.AgentRecord
	if strings.TrimSpace(rec.Name) == "" {
		return AgentRecord{}, 0, Error("import: name is required")
	}
	if strings.TrimSpace(rec.OrchestratorPrompt) == "" {
		return AgentRecord{}, 0, Error("import: orchestrator_prompt is required")
	}
	rec.ID = ""
	rec.Owner = owner
	rec.OwnedBy = ""
	rec.Created = time.Time{}
	rec.Updated = time.Time{}
	// Recipes carry tools inline (both pre- and post-flatten exports). Hold
	// them aside: they fold into the unified store AFTER the save assigns the
	// reborn agent its id — the record itself stays tool-free.
	inlineTools := rec.Tools
	rec.Tools = nil
	saved, err := saveAgent(udb, rec)
	if err != nil {
		return AgentRecord{}, 0, err
	}
	foldImportedTools(udb, owner, &saved, inlineTools)
	subCount := 0
	for _, s := range imp.SubAgents {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.OrchestratorPrompt) == "" {
			Log("[orchestrate.agents] import: skipping sub-agent with missing name/prompt under %q", saved.Name)
			continue
		}
		s.ID = ""
		s.Owner = owner
		s.OwnedBy = saved.ID
		s.Created = time.Time{}
		s.Updated = time.Time{}
		subTools := s.Tools
		s.Tools = nil
		savedSub, serr := saveAgent(udb, s)
		if serr != nil {
			Log("[orchestrate.agents] import: sub-agent %q failed: %v", s.Name, serr)
			continue
		}
		foldImportedTools(udb, owner, &savedSub, subTools)
		subCount++
	}
	return saved, subCount, nil
}

// foldImportedTools lands a recipe's inline tools in the unified store scoped
// to the reborn agent, via the same conflict policy the flatten migration
// uses (identical dup → merge, diverged → orphan with provenance).
func foldImportedTools(udb Database, owner string, saved *AgentRecord, tools []TempTool) {
	if len(tools) == 0 {
		return
	}
	carrier := *saved
	carrier.Tools = tools
	moved, merged, orphaned := foldAgentToolsIntoStore(udb, owner, &carrier)
	if orphaned > 0 {
		Log("[orchestrate.agents] import %q: %d tool(s) diverged from your existing tools — imported copies are in Orphaned tools", saved.Name, orphaned)
	}
	_ = moved
	_ = merged
}

// safeFilename returns a slug suitable for the Content-Disposition
// filename header. Strips anything that isn't alphanumeric, dash, or
// underscore; collapses runs to single dashes; falls back to "agent"
// when the result would be empty.
func safeFilename(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'
		if ok {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return out
}

func (T *OrchestrateApp) handleAgentOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Path: /api/agents/<id>  or  /api/agents/<id>/clone
	rest := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	var id, action string
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		id = rest[:slash]
		action = rest[slash+1:]
	} else {
		id = rest
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}

	if action == "clone" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Name    string `json:"name,omitempty"`
			Promote bool   `json:"promote,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		clone, err := cloneAgent(udb, id, user, body.Name, body.Promote)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(clone)
		return
	}
	if action == "facts" {
		T.handleAgentFacts(w, r, user, id)
		return
	}
	if action == "notes" {
		T.handleAgentNotes(w, r, user, id)
		return
	}
	if action == "memaudit" {
		T.handleAgentMemoryAudit(w, r, user, id)
		return
	}
	if action == "memsearch" {
		T.handleAgentMemorySearch(w, r, user, id)
		return
	}
	if action == "guardrails" {
		T.handleAgentGuardrails(w, r, user, id)
		return
	}
	if action == "decline-suggest" {
		T.handleAgentDeclineSuggest(w, r, user, id)
		return
	}
	if action == "guardrail-test" {
		T.handleAgentGuardrailTest(w, r, user, id)
		return
	}
	if action == "inferred" {
		T.handleAgentInferredList(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "inferred/") {
		chunkID := strings.TrimPrefix(action, "inferred/")
		T.handleAgentInferredDelete(w, r, user, id, chunkID)
		return
	}
	if action == "graph" {
		T.handleAgentGraph(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "graph/entity/") {
		rest := strings.TrimPrefix(action, "graph/entity/")
		// Entity IDs are "<kind>:<slug>" — never contain a slash — so a
		// trailing /attr or /alias unambiguously selects the sub-action.
		switch {
		case strings.HasSuffix(rest, "/attr"):
			T.handleAgentGraphAttrDelete(w, r, user, id, strings.TrimSuffix(rest, "/attr"))
		case strings.HasSuffix(rest, "/alias"):
			T.handleAgentGraphAliasDelete(w, r, user, id, strings.TrimSuffix(rest, "/alias"))
		default:
			T.handleAgentGraphEntityDelete(w, r, user, id, rest)
		}
		return
	}
	if action == "graph/edge" {
		T.handleAgentGraphEdgeDelete(w, r, user, id)
		return
	}
	if action == "knowledge" {
		T.handleAgentKnowledge(w, r, user, id)
		return
	}
	if action == "phantom-sessions" {
		T.handleAgentPhantomSessions(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "phantom-sessions/") {
		// /api/agents/{id}/phantom-sessions/{session_id}?chat_id=<chatID>
		// reads a single phantom-owned session out of the per-chat
		// sub-store. Read-only; deletion can be added later if needed.
		sid := strings.TrimPrefix(action, "phantom-sessions/")
		T.handleAgentPhantomSessionOne(w, r, user, id, sid)
		return
	}
	if action == "lock" {
		T.handleAgentLock(w, r, user, id)
		return
	}
	if action == "knowledge/auto-inferred" {
		T.handleAgentKnowledgeAutoInferredWipe(w, r, user, id)
		return
	}
	if action == "knowledge/scaffold-collection" {
		T.handleAgentKnowledgeScaffoldCollection(w, r, user, udb, id)
		return
	}
	if action == "knowledge/upload" {
		T.handleAgentKnowledgeUpload(w, r, user, id)
		return
	}
	if action == "knowledge/sources" {
		T.handleAgentKnowledgeSources(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "knowledge/sources/") {
		reportID := strings.TrimPrefix(action, "knowledge/sources/")
		T.handleAgentKnowledgeSourceDelete(w, r, user, id, reportID)
		return
	}
	if action == "eval" {
		// Dispatch into the eval-harness handler via a synthetic
		// path so handleAgentEval's TrimPrefix logic still works.
		r.URL.Path = "/api/agents/" + id + "/eval"
		_ = user // (used implicitly by handleAgentEval via RequireUser)
		T.handleAgentEval(w, r)
		return
	}
	if action == "export" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		payload, ok := buildAgentExport(udb, id, user)
		if !ok {
			http.NotFound(w, r)
			return
		}
		filename := safeFilename(payload.Name) + ".agent.json"
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+filename+`"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
		return
	}
	if action != "" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		a, ok := loadAgent(udb, id)
		if !ok || (a.Owner != user && a.Owner != seedOwner) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Flattened namespace: the record stores no tools; the GET response
		// synthesizes the `tools` array from the unified store (rows scoped to
		// this agent) as a VIEW — the Tools modal renders it unchanged. A fork
		// between a pool copy and a record copy is structurally impossible
		// now, so the old pool_diverged_tools computation is gone. The POST
		// below strips Tools before save, so the fetch-modify-post round-trip
		// can't write the view back into storage.
		a.Tools = toolsOfScoped(AgentScopedTools(udb, user, a.ID))
		_ = json.NewEncoder(w).Encode(a)
	case http.MethodPost:
		// PARTIAL update of one existing agent. The full edit form posts the
		// whole record to /api/agents (handleAgentList); single-field surfaces
		// like the dispatch-allowlist ChipPicker POST just their field HERE.
		// Without this case the POST fell to default → 405, so the
		// allowed_dispatch_targets picker silently never saved (the dispatch
		// allowlist "didn't work"). Decoding the posted body INTO the loaded
		// record merges: present fields overwrite, absent fields keep their
		// stored value — and Locked (owned by the lock icon) is preserved since
		// the partial body never carries it.
		existing, ok := loadAgent(udb, id)
		if !ok || (existing.Owner != "" && existing.Owner != user && existing.Owner != seedOwner) {
			http.NotFound(w, r)
			return
		}
		locked := existing.Locked // owned by the lock icon, not the form/picker
		if err := json.NewDecoder(r.Body).Decode(&existing); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		existing.ID = id
		existing.Owner = user
		existing.Locked = locked
		// Flattened namespace: tools live in the unified store, and the GET
		// view synthesizes them onto the record — never write that view back.
		existing.Tools = nil
		saved, err := saveAgent(udb, existing)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(saved)
	case http.MethodDelete:
		if err := deleteAgent(udb, id, user); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// (poolDivergedTools is gone: under the flattened namespace a pool/record
// fork is structurally impossible — one name is one store row.)
// tempToolDefEqual compares two TempTool definitions ignoring the
// user-governance / provenance fields (lock, disable, builder-only, trial
// clock) that legitimately differ between a pool copy and a record copy of
// the same tool. Only a difference in what the tool DOES counts as a fork.
func tempToolDefEqual(a, b TempTool) bool {
	neutralize := func(t TempTool) TempTool {
		t.Locked, t.Disabled, t.BuilderOnly, t.Trial = false, false, false, false
		t.TrialSince = time.Time{}
		return t
	}
	return reflect.DeepEqual(neutralize(a), neutralize(b))
}

// handleAgentLock toggles the per-agent edit/delete lock — POST /api/agents/{id}/lock
// {locked}. This is the HUMAN control (the editor's lock icon), so it's owner-
// gated only; the agent-CRUD tools enforce the lock, they don't set it. Locked
// is changed ONLY here — the main agent save preserves the stored value — so the
// icon is the single source of truth and a form save can't clobber it.
func (T *OrchestrateApp) handleAgentLock(w http.ResponseWriter, r *http.Request, user, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	udb := UserDB(T.DB, user)
	a, ok := loadAgent(udb, id)
	// Seeds load with an empty Owner until first shadowed; treat that as the
	// caller's own. A non-seed must already belong to the caller.
	if !ok || (a.Owner != "" && a.Owner != user) {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Locked bool `json:"locked"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	a.Owner = user
	a.Locked = req.Locked
	if _, err := saveAgent(udb, a); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"locked": a.Locked})
}

// newlyHidden reports whether this save is the one turning Hidden ON — a new
// record arriving hidden, or an existing record whose stored copy was visible.
//
// Separates "apply a sensible default at the moment the user hides an agent"
// from "re-apply it forever", which is the difference between a default and an
// override the user cannot escape.
func newlyHidden(db Database, a AgentRecord) bool {
	if db == nil || strings.TrimSpace(a.ID) == "" {
		return true // brand-new record: the default applies
	}
	var prior AgentRecord
	if !db.Get(agentsTable, a.ID, &prior) {
		return true // no stored copy yet
	}
	return !prior.Hidden
}
