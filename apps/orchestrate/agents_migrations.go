package orchestrate

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

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
	// Scan scope belongs on this list for the exact reason the comment above
	// describes: the guardrails endpoint writes it to the shadow, so a seed
	// rebuilt without it reads back empty and the boot-time shadow rewrite then
	// erases it for good. Builder is both the agent most likely to fetch
	// something and the one an owner most wants watched.
	seed.ScanToolResults = shadow.ScanToolResults
	seed.ScanToolsAdd = shadow.ScanToolsAdd
	seed.ScanToolsSkip = shadow.ScanToolsSkip
	seed.ScanAction = shadow.ScanAction
	seed.ScanBlockTools = shadow.ScanBlockTools
	seed.ScanAppealable = shadow.ScanAppealable
	seed.ScanTightenDisabled = shadow.ScanTightenDisabled
	seed.ScanTrustedSources = shadow.ScanTrustedSources
	// Reachability over the inbound MCP server. Deployment state by the same
	// argument as LeadModel above: the editor RENDERS this toggle on Builder,
	// so leaving it off this list made it save and read back false every
	// time — the toggle looked like it refused to stay on. Worse than the
	// LeadModel case, because migrateBuilderShadows writes the rebuilt record
	// back over the shadow at boot, so a restart made the loss permanent.
	seed.MCPExposed = shadow.MCPExposed
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

// migrateSeedShadowOverlays stamps every pre-overlay seed shadow with the
// fields it had actually decided (see agent_overlay.go).
//
// A shadow written before overlays carries no list, so its overrides have to
// be inferred by diffing it against the seed. That inference is correct only
// against the seed the shadow was written from, and running it lazily at read
// time compares against the CURRENT seed instead: a field the framework
// changed after the shadow was written looks exactly like a field the user
// edited, so it would freeze. Doing it once, now, fixes the comparison at the
// moment the deployment upgrades, which is the last moment the two are still
// in step.
//
// The effect on the user is nothing. Every difference their shadow has today
// is preserved as an override, so their agents look exactly as they did
// before; what changes is that every field they never touched starts tracking
// the framework again.
func (T *OrchestrateApp) migrateSeedShadowOverlays() {
	if T == nil || T.DB == nil || AuthDB == nil {
		return
	}
	authDB := AuthDB()
	if authDB == nil {
		return
	}
	stamped := 0
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(T.DB, u.Username)
		if udb == nil {
			continue
		}
		for _, seed := range seedAgents() {
			if isBuilderAgent(seed.ID) {
				continue // resolves through applyBuilderDeploymentState
			}
			var shadow AgentRecord
			if !udb.Get(agentsTable, seed.ID, &shadow) {
				continue
			}
			if shadow.OverlayRev != 0 {
				continue
			}
			shadow.OverriddenFields = agentOverrides(seed, shadow)
			shadow.OverlayRev = 1
			udb.Set(agentsTable, seed.ID, shadow)
			stamped++
			Log("[orchestrate.migrate] seed overlay: %s for user=%q keeps %d decision(s), the rest now tracks",
				seed.ID, u.Username, len(shadow.OverriddenFields))
		}
	}
	if stamped > 0 {
		Log("[orchestrate.migrate] migrateSeedShadowOverlays: stamped %d shadow(s)", stamped)
	}
}
