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
// loadAgent applies it on every read of Builder, so the stored shadow keeps
// only what the owner saved and nothing rewrites it. A startup pass used to
// write the rebuilt record back over every shadow at each boot; any field
// missing from this list was then erased for good at the next restart.
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
	// And whether the lead designs while a worker writes the apps
	// (builder_app_split.go): the same choice, its second field.
	seed.LeadPlans = shadow.LeadPlans
	seed.LeadReview = shadow.LeadReview
	// The owner's enforced limits. These are deployment state in the strictest
	// sense — the framework owns Builder's PROMPT, the deployment owns what it
	// is allowed to do — and they are owner-only fields no agent edit path can
	// reach, so rebasing them onto code protects nothing and destroys the one
	// thing the owner wrote by hand.
	//
	// Left off this list they saved and read back empty, silently: the
	// guardrails endpoint wrote them to the shadow, loadAgent rebuilt Builder
	// from the seed without them, and a boot pass (since removed) then wrote
	// that stripped copy BACK over the shadow, so a restart erased them for
	// good. The visible symptom was "exceptions won't save"; the real one was
	// that guardrails never applied to Builder AT ALL — no rules, no hooks, no
	// declines — which is the agent with authoring access and therefore the one
	// an owner is most likely to want limits on.
	seed.Guardrails = shadow.Guardrails
	seed.GuardrailHooks = shadow.GuardrailHooks
	seed.GuardrailFailClosed = shadow.GuardrailFailClosed
	seed.GuardrailDepth = shadow.GuardrailDepth
	seed.GuardrailDeclines = shadow.GuardrailDeclines
	seed.GuardrailsDisabled = shadow.GuardrailsDisabled
	seed.GuardrailExceptions = shadow.GuardrailExceptions
	seed.AuthorizedIdentities = shadow.AuthorizedIdentities
	// Scan scope belongs on this list for the exact reason the comment above
	// describes: the guardrails endpoint writes it to the shadow, so a seed
	// rebuilt without it reads back empty. Builder is both the agent most likely to fetch
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
	// time — the toggle looked like it refused to stay on.
	seed.MCPExposed = shadow.MCPExposed
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
// user's own store. Runs once (runMigrations) and says how many it dropped.
func (T *OrchestrateApp) dropLegacyOperator() int {
	if T == nil || T.DB == nil || AuthDB == nil {
		return 0
	}
	authDB := AuthDB()
	if authDB == nil {
		return 0
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
	return dropped
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
// sentinel so all approved persistent tools auto-load. Shadows already at
// empty (or no shadow at all) are skipped. Runs once (runMigrations) and says
// how many it reset.
func (T *OrchestrateApp) migrateSeedChatFrozenAllowedTools() int {
	if T == nil || T.DB == nil || AuthDB == nil {
		return 0
	}
	authDB := AuthDB()
	if authDB == nil {
		return 0
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
	return cleared
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

// migrateRetiredAuthorFlag moves every agent off the retired Author flag.
//
// Author gave any agent Builder's authoring catalog. It is retired because those
// agents held the tools without Builder's doctrine, and an authoring agent that
// faces a channel is attack surface (see authoring_requester.go). What such an
// agent keeps is the ability to get things BUILT: it may hand the work to
// Builder (AllowBuilderDispatch), which asks through it when it needs the user.
// Consult the Lead was on for it by default because it could author; an agent
// that never chose otherwise keeps it on rather than losing it silently.
//
// Once, deployment-wide, through the migration runner. agentCanAuthor ignores
// the flag already, so a record this misses (an import, a restored backup)
// grants nothing; it just does not gain the dispatch grant.
// migrateOwnAgentCardPaths moves the dashboard cards people switched on for
// their own agents from the full console (/orchestrate/?agent=<id>) to the
// restricted chat (/agents/<id>), so a card they had keeps its place instead
// of vanishing under its old address.
func (T *OrchestrateApp) migrateOwnAgentCardPaths() {
	NewMigrationRunner("orchestrate", "").Once("own_agent_card_paths:v1", func() int {
		if AuthDB == nil {
			return 0
		}
		authDB := AuthDB()
		if authDB == nil {
			return 0
		}
		changed := 0
		for _, u := range AuthListUsers(authDB) {
			changed += RenameDashboardCardPaths(u.Username, func(path string) string {
				if id, ok := strings.CutPrefix(path, "/orchestrate/?agent="); ok && id != "" && !strings.ContainsAny(id, "&?/=") {
					return ownAgentCardPath(id)
				}
				return path
			})
		}
		return changed
	})
}

func (T *OrchestrateApp) migrateRetiredAuthorFlag() {
	NewMigrationRunner("orchestrate", "").Once("retire_author_flag:v1", func() int {
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
				if !udb.Get(agentsTable, k, &a) || !a.Author {
					continue
				}
				udb.Set(agentsTable, k, retireAuthorFlag(a))
				changed++
			}
		}
		return changed
	})
}

// retireAuthorFlag is the per-record half of migrateRetiredAuthorFlag.
func retireAuthorFlag(a AgentRecord) AgentRecord {
	if !a.Author {
		return a
	}
	a.Author = false
	if !isBuilderAgent(a.ID) {
		a.AllowBuilderDispatch = true
		if a.ConsultLead == "" {
			a.ConsultLead = settingOn
		}
	}
	return a
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
//
// Runs once (runMigrations) and says how many it stamped. A pre-overlay shadow
// that arrives later (a restored backup) is resolved by inference at read time
// until it is next saved, which stamps it.
func (T *OrchestrateApp) migrateSeedShadowOverlays() int {
	if T == nil || T.DB == nil || AuthDB == nil {
		return 0
	}
	authDB := AuthDB()
	if authDB == nil {
		return 0
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
			shadow.OverriddenFields = agentOverrides(seed, shadow, frameworkOwnedSeedFields(seed.ID))
			shadow.OverlayRev = 1
			udb.Set(agentsTable, seed.ID, shadow)
			stamped++
			Log("[orchestrate.migrate] seed overlay: %s for user=%q keeps %d decision(s), the rest now tracks",
				seed.ID, u.Username, len(shadow.OverriddenFields))
		}
	}
	return stamped
}
