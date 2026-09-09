package orchestrate

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
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
	// The fix after that was a hand-written list of fields to refresh from
	// the seed, which grew a rule per bug report and covered only the fields
	// somebody had already noticed. So the SEED is the base now, and the
	// shadow contributes exactly the fields the user decided for themselves
	// (AllowedTools, Rules, think budget, attached skills/collections,
	// exposure, and the rest). Everything else tracks. Builder above is the
	// stricter sibling: fully locked, so it rebases everything except a short
	// deployment list onto code.
	if seed, ok := seedAgentByID(id); ok {
		var shadow AgentRecord
		if db.Get(agentsTable, id, &shadow) {
			// The shadow is an OVERLAY: the framework's record wearing the
			// user's decisions. Anything they never decided tracks the seed,
			// which is what stops a fix from stopping at whoever happened to
			// approve a tool once. resolveSeedShadow carries the reasoning,
			// including which fields the framework keeps for itself.
			merged := resolveSeedShadow(seed, shadow)
			merged = selfHealAllowedTools(db, merged)
			return enforceSubAgentPosture(applyLegacyMode(merged)), true
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

// saveAgent upserts an agent record, stamping timestamps + ID on new
// records. Owner must be set by the caller. Seed-IDs are written
// under the same ID as user-owned shadow records (no forking) — this
// is what makes "Edit a seed, then Revert" work.
// saveAgent writes an agent record, PRESERVING the stored Locked flag.
//
// Locked is the human's lock icon and belongs to handleAgentLock alone. That
// rule used to be enforced by each HTTP save path reading the stored value back
// before calling here — three places, correct in all three, and structurally
// unable to cover a fourth. A path that built a record and called saveAgent
// directly cleared an admin's lock with no error and no log, which is the worst
// shape a permission bug takes.
//
// Enforced here now, so the rule holds for every caller that exists and every
// caller that does not yet. setAgentLocked is the one door through it.
func saveAgent(db Database, a AgentRecord) (AgentRecord, error) {
	return writeAgent(db, a, false)
}

// setAgentLocked is the ONLY way to change an agent's lock. Separate function
// rather than a flag on saveAgent, so the exception is something a caller has
// to reach for by name — grep for it and the answer is the lock handler.
func setAgentLocked(db Database, a AgentRecord, locked bool) (AgentRecord, error) {
	a.Locked = locked
	return writeAgent(db, a, true)
}

// writeAgent is the single store write. maySetLocked is false for everything
// except setAgentLocked.
func writeAgent(db Database, a AgentRecord, maySetLocked bool) (AgentRecord, error) {
	if db == nil {
		return a, fmt.Errorf("db not initialized")
	}
	if !maySetLocked && a.ID != "" {
		// A record with no stored counterpart is a creation; there is no lock
		// to preserve and the caller's value (normally false) stands.
		if existing, ok := loadAgent(db, a.ID); ok {
			a.Locked = existing.Locked
		}
	}
	// What the CALLER decided, recorded before the invariants below get a
	// vote. A rule that fires on save is the framework's decision, not the
	// user's, and recording it as an override would freeze that field against
	// every future framework change. The Locked preservation above runs first
	// on purpose: an unchanged lock is not a user decision either, but losing
	// it would be.
	a = recordSeedOverrides(a)
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
	// Hand back what a load would now give, not the row that went to storage.
	// For a seed shadow those differ: the row is a full snapshot, while the
	// agent is the seed wearing this record's overrides, so returning the row
	// would report values a subsequent read does not agree with.
	if seed, ok := seedAgentByID(a.ID); ok && !isBuilderAgent(a.ID) {
		return resolveSeedShadow(seed, a), nil
	}
	return a, nil
}

// recordSeedOverrides stamps a seed shadow with the fields it has decided for
// itself, so every OTHER field keeps tracking the seed. A record that is not a
// seed shadow is returned untouched: a user's own agent is a whole agent, not
// an overlay on anything.
//
// Recomputed on every save rather than accumulated, which gives revert for
// free: set a field back to the framework's value and it stops being an
// override, so the next improvement to it lands.
//
// Builder is excluded because it resolves through applyBuilderDeploymentState,
// the inverse policy: an allowlist of what the deployment owns rather than a
// list of what the user changed.
func recordSeedOverrides(a AgentRecord) AgentRecord {
	if isBuilderAgent(a.ID) {
		return a
	}
	seed, ok := seedAgentByID(a.ID)
	if !ok {
		return a
	}
	a.OverriddenFields = agentOverrides(seed, a)
	a.OverlayRev = 1
	return a
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
	_ = pipelineScheduleGuard // see pipelineDeleted: the same rule for a pipeline target
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
