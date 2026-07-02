// appliance_memory.go — per-appliance agent Memory surface.
//
// Each servitor appliance instantiates a shared orchestrate agent TEMPLATE
// (the investigator) under a synthetic scope user "app:servitor:<id>". The
// appliance's Explicit Memory (Saved facts), Graph Memory, and Reference
// Memory therefore live in orchestrate's stores keyed by (scope, agentID) —
// the same layered model orchestrate agents use — instead of servitor's flat
// ssh_facts. This file mounts the SAME editable Memory modal apps/agents
// uses (orchestrate.AgentMemoryModalScript) pointed at a per-appliance scope,
// and routes its data endpoints to orchestrate's …ForScope handlers.
//
// Scope only — no runtime swap. Servitor still runs its own RunAgentLoop for
// probing today; the lead-migration slice swaps that to a scoped run that
// records natively into this same memory. Until then the modal reflects
// whatever has been seeded/recorded into the scope (empty on a fresh box).
package servitor

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// servitorInvestigatorAgentID is the stable ID of the orchestrate agent
// template backing every appliance's memory. ID convention: app-<app>-<role>.
const servitorInvestigatorAgentID = "app-servitor-investigator"

func init() {
	// Register the template so loadAgent resolves it (via the seed fallback)
	// for any "app:servitor:<id>" scope. Hidden keeps it out of public agent
	// pickers; it still resolves for scoped memory + future dispatch. The prompt
	// is the SAME static investigator core the live buildLeadSystemPrompt uses
	// (leadStaticGuidance), so when the runtime swaps to a scoped run there's no
	// behavior drift; the per-appliance persona/docs/facts/rules layer on from
	// the scope's memory + the run message rather than being baked here.
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID:          servitorInvestigatorAgentID,
		OwningApp:   "Servitor",
		Name:        "Servitor Investigator",
		Description: "Per-appliance investigator template. Memory (facts, graph, reference) is layered per appliance scope.",
		Prompt:      investigatorTemplatePrompt(),
		Hidden:      true,
		// Explicit Memory is the always-in-prompt SHORTCUTS layer: working
		// access/operate commands (record_technique) + gotchas (note_lesson).
		// Specific one-off findings go to Reference Memory; structured facts +
		// topology go to the graph.
		MemoryMode: "shortcuts",
	})
}

// investigatorTemplatePrompt is the appliance-GENERIC system prompt for the
// template agent: a generic role intro plus the shared leadStaticGuidance
// (web.go) the live investigator already uses. The specific appliance, its
// knowledge docs, its known facts/techniques/rules, and the question are NOT
// baked in here — at run time they come from the scope's layered memory and the
// investigation request, which is exactly what lets one template serve every
// appliance.
func investigatorTemplatePrompt() string {
	var b strings.Builder
	b.WriteString("You are the Knowledge Manager and investigator for an appliance (a server or system) under investigation.\n\n")
	b.WriteString("Your job is to answer questions about THIS system with verified, specific facts — never estimates, training knowledge, or guesses. ")
	b.WriteString("You dispatch a worker agent (full SSH access) to retrieve anything you cannot answer from verified records, and you maintain structured knowledge docs about the system.\n\n")
	b.WriteString("The specific appliance, its connection details, your structured knowledge docs, and any standing user instructions are provided to you per run. What is already known about this system — facts, working techniques, prior discoveries — is in your memory; consult it before probing.\n\n")
	leadStaticGuidance(&b)
	return b.String()
}

// applianceMemoryModalScript registers the shared editable Memory modal under
// servitor's own client action. The base resolver runs at open: servitorMemBase()
// (web_assets.go) returns the selected appliance's relative endpoint prefix, or
// null (nothing picked → alert + abort).
var applianceMemoryModalScript = orchestrate.AgentMemoryModalScript("servitor_appliance_memory", "servitorMemBase()")

// cachedServitorOrch holds the registered orchestrate app (registry is fixed
// at runtime, so cache after first hit).
var cachedServitorOrch *orchestrate.OrchestrateApp

func servitorOrch() *orchestrate.OrchestrateApp {
	if cachedServitorOrch != nil {
		return cachedServitorOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedServitorOrch = o
	return cachedServitorOrch
}

// applianceMemScope maps an appliance to its synthetic scope user.
func applianceMemScope(applianceID string) string {
	return "app:servitor:" + applianceID
}

// clearApplianceScopedMemory wipes the appliance's orchestrate-scoped memory
// (graph entity, Reference Memory, Explicit facts) and resets the one-time seed
// marker, so servitor's "Clear Memory" is consistent across the dual-write
// split — clearing empties BOTH the legacy ssh_* stores and the scope the
// Memory modal reads. Best-effort: orchestrate may be unreachable in odd setups.
func clearApplianceScopedMemory(udb Database, applianceID string) {
	if udb == nil || applianceID == "" {
		return
	}
	udb.Unset(factsSeededTable, applianceID) // let a future Memory open re-seed
	if orch := servitorOrch(); orch != nil {
		_ = orch.WipeScopedMemory(orchestrate.AgentScope{
			AgentID:   servitorInvestigatorAgentID,
			ScopeUser: applianceMemScope(applianceID),
		})
	}
}

// buildScopedLeadMessage flattens the chat conversation into the single run
// message the scoped investigator receives (the rich scoped entry takes one
// Message, not a []Message). All the per-appliance CONTEXT rides on the system
// prompt (buildLeadSystemPrompt via SystemPromptOverride); this carries only the
// conversation — the current request plus any prior turns for continuity.
func buildScopedLeadMessage(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}
	if len(messages) == 1 {
		return strings.TrimSpace(messages[0].Content)
	}
	var b strings.Builder
	b.WriteString("## Conversation so far\n\n")
	for _, m := range messages[:len(messages)-1] {
		who := m.Role
		switch m.Role {
		case "user":
			who = "User"
		case "assistant":
			who = "You"
		}
		if c := strings.TrimSpace(m.Content); c != "" {
			b.WriteString("**" + who + ":** " + c + "\n\n")
		}
	}
	b.WriteString("## Current request\n\n")
	b.WriteString(strings.TrimSpace(messages[len(messages)-1].Content))
	return b.String()
}

// scopedApplianceFacts returns the appliance's facts as a key→value map, read
// from the scope's graph entity. It first runs the one-time seed bridge so any
// pre-existing ssh_facts are migrated into the scope BEFORE the read — keeping
// continuity at the moment the read path cuts over from ssh_facts to the scope.
// nil when nothing is known yet.
func scopedApplianceFacts(udb Database, a Appliance) map[string]string {
	if udb == nil || a.ID == "" {
		return nil
	}
	seedApplianceMemoryFromFacts(udb, a) // idempotent; ensures pre-cutover facts are present
	orch := servitorOrch()
	if orch == nil {
		return nil
	}
	scope := orchestrate.AgentScope{AgentID: servitorInvestigatorAgentID, ScopeUser: applianceMemScope(a.ID)}
	e, ok := orch.ScopedGraphEntity(scope, a.ID)
	if !ok {
		return nil
	}
	return e.Attrs
}

// scopedGraphBlock renders the appliance's scoped graph (all entities + attrs +
// relationships) for prompt injection, so the investigator and worker actually
// SEE the topology that link_entities builds — otherwise the graph would be
// display-only. Empty when the graph has nothing yet.
func scopedGraphBlock(a Appliance) string {
	if a.ID == "" {
		return ""
	}
	orch := servitorOrch()
	if orch == nil {
		return ""
	}
	scope := orchestrate.AgentScope{AgentID: servitorInvestigatorAgentID, ScopeUser: applianceMemScope(a.ID)}
	return orch.ScopedGraphSummary(scope)
}

// scopedFactsBlock renders the appliance's scoped facts as sorted "- key: value"
// lines for the system prompt (replacing the old formatFactsWithAge over
// ssh_facts). Per-fact age is intentionally not carried — graph attrs have no
// per-attr timestamp; the prompt leans on the standing "always re-probe live
// state" rule instead.
func scopedFactsBlock(udb Database, a Appliance) string {
	attrs := scopedApplianceFacts(udb, a)
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("- " + k + ": " + attrs[k] + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// recordScopedApplianceFact mirrors a store_fact into the appliance's scoped
// GRAPH memory (the appliance entity gains the fact as an attr), so the lead's
// and worker's live recordings land in the same per-appliance memory the Memory
// modal and a future scoped run read — alongside the existing ssh_facts write
// (lead migration, slice 1: dual-write). Short-TTL (ephemeral) facts are
// skipped, matching the one-time seed bridge. Best-effort: a failure leaves the
// ssh_facts write as the source of truth.
func recordScopedApplianceFact(a Appliance, key, value, ttl string) {
	if a.ID == "" || key == "" || ttl == "short" || strings.TrimSpace(value) == "" {
		return
	}
	orch := servitorOrch()
	if orch == nil {
		return
	}
	name := a.Name
	if name == "" {
		name = a.ID
	}
	scope := orchestrate.AgentScope{AgentID: servitorInvestigatorAgentID, ScopeUser: applianceMemScope(a.ID)}
	_ = orch.SeedScopedGraphEntity(scope, "appliance", name, []string{a.ID}, map[string]string{key: value})
}

// recordScopedLink records a relationship (subject→relation→object, with
// optional subject attrs) in the appliance's scoped graph, so the worker builds
// a real multi-entity system map instead of piling every fact onto the single
// appliance node. Best-effort.
func recordScopedLink(a Appliance, subjectKind, subject string, subjectAttrs map[string]string, relation, objectKind, object, note string, replace bool) error {
	if a.ID == "" {
		return nil
	}
	orch := servitorOrch()
	if orch == nil {
		return nil
	}
	scope := orchestrate.AgentScope{AgentID: servitorInvestigatorAgentID, ScopeUser: applianceMemScope(a.ID)}
	return orch.SeedScopedGraphLink(scope, subjectKind, subject, subjectAttrs, relation, objectKind, object, note, replace)
}


// recordScopedExplicit records a SHORTCUT (a working access/operate command) or
// a LESSON (a gotcha) into the scope's EXPLICIT Memory — the always-in-prompt
// layer — because both are operational knowledge the investigator wants in view
// every time, without having to search for it. Specific one-off findings go to
// Reference Memory instead. StoreMemoryFact dedups/supersedes, so re-recording
// the same entry is idempotent. Surfaces under the "Shortcuts" block.
func recordScopedExplicit(a Appliance, note string) {
	if a.ID == "" || strings.TrimSpace(note) == "" {
		return
	}
	orch := servitorOrch()
	if orch == nil {
		return
	}
	scope := orchestrate.AgentScope{AgentID: servitorInvestigatorAgentID, ScopeUser: applianceMemScope(a.ID)}
	_ = orch.SeedScopedMemory(scope, note)
}

// recordScopedReference mirrors a prose recording (a technique, a discovery, a
// lesson) into the appliance's scoped REFERENCE Memory — the derived/searchable
// layer the Memory modal shows and the scoped agent recalls via memory_search.
// SeedScopedKnowledge ingests under an orch-know reportID, which chunkProvenance
// classifies as "derived", so it lands in Reference Memory (not curated
// Knowledge). Alongside the existing ssh_* write (lead migration, slice 1:
// dual-write). topic shards retrieval (techniques / discoveries / lessons).
func recordScopedReference(ctx context.Context, a Appliance, topic, title, body string) {
	if a.ID == "" || strings.TrimSpace(body) == "" {
		return
	}
	orch := servitorOrch()
	if orch == nil {
		return
	}
	scope := orchestrate.AgentScope{AgentID: servitorInvestigatorAgentID, ScopeUser: applianceMemScope(a.ID)}
	_ = orch.SeedScopedKnowledge(ctx, scope, topic, title, body)
}

// factsSeededTable marks (per appliance) that the one-time ssh_facts → scoped
// Explicit Memory bridge has run, so it doesn't re-scan on every modal open.
const factsSeededTable = "ssh_facts_seeded"

// seedApplianceMemoryFromFacts is a one-time bridge: it copies an appliance's
// existing ssh_facts into the orchestrate-scoped GRAPH memory the Memory modal
// reads, so the modal shows real data before lead migration starts recording
// natively. ssh_facts are keyed key→value pairs, so their faithful home is a
// single appliance ENTITY whose attributes are those facts — deterministic
// overwrite by key, no LLM cost, and ready to grow relationships later (the
// shape the lead will record into too). It runs ONCE per appliance
// (marker-guarded) — a snapshot, not a live sync, and the marker also means
// entries the user later deletes from the modal aren't resurrected from
// ssh_facts. Short-TTL (ephemeral) facts are skipped — they're meant to expire,
// not become durable memory. Best-effort: a failure just leaves the scope as-is.
func seedApplianceMemoryFromFacts(udb Database, a Appliance) {
	if udb == nil || a.ID == "" {
		return
	}
	var seeded bool
	if udb.Get(factsSeededTable, a.ID, &seeded) {
		return // already bridged once
	}
	orch := servitorOrch()
	if orch == nil {
		return
	}
	attrs := make(map[string]string)
	for _, f := range factsForAppliance(udb, a.ID) {
		if f.TTL == "short" || f.Key == "" {
			continue // ephemeral or unkeyed — skip
		}
		if v := strings.TrimSpace(f.Value); v != "" {
			attrs[f.Key] = v
		}
	}
	if len(attrs) == 0 {
		// Nothing to copy; mark anyway so an empty appliance doesn't re-scan.
		udb.Set(factsSeededTable, a.ID, true)
		return
	}
	name := a.Name
	if name == "" {
		name = a.ID
	}
	scope := orchestrate.AgentScope{AgentID: servitorInvestigatorAgentID, ScopeUser: applianceMemScope(a.ID)}
	if err := orch.SeedScopedGraphEntity(scope, "appliance", name, []string{a.ID}, attrs); err != nil {
		return // leave unmarked so a later open retries
	}
	udb.Set(factsSeededTable, a.ID, true)
}

// handleApplianceMemory serves the per-appliance agent Memory surface at
//
//	/servitor/api/appliances/<applianceID>/<suffix>
//
// dispatching each suffix to orchestrate's …ForScope handlers (which read +
// write the appliance's scoped memory). Ownership: the appliance must exist in
// the requesting user's per-user store; the orchestrate handlers layer a
// session gate on top. The suffix routing mirrors apps/agents exactly so the
// shared modal hits the same endpoints either way.
func (T *Servitor) handleApplianceMemory(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/appliances/")
	applianceID, suffix, found := strings.Cut(rest, "/")
	if !found || applianceID == "" || suffix == "" {
		http.NotFound(w, r)
		return
	}
	// Resolve own OR shared — a shared appliance's memory (keyed by appliance ID,
	// global) is visible to every user. Seeding/reads use the OWNER's store so
	// there's one source of truth.
	a, _, ownerUDB, found := T.resolveAppliance(userID, udb, applianceID)
	if !found {
		http.NotFound(w, r)
		return
	}
	udb = ownerUDB
	orch := servitorOrch()
	if orch == nil {
		http.Error(w, "orchestrate unavailable", http.StatusInternalServerError)
		return
	}
	scope := applianceMemScope(applianceID)
	id := servitorInvestigatorAgentID

	switch {
	case suffix == "facts":
		orch.PublicHandleAgentFactsForScope(w, r, scope, id)
	case suffix == "graph":
		// First graph read bridges the appliance's existing ssh_facts into
		// the scope as an entity (one-time; see the func doc) so the Graph
		// Memory section isn't empty. Seed before serving so the response
		// includes it (no race with a separate facts fetch).
		if r.Method == http.MethodGet {
			seedApplianceMemoryFromFacts(udb, a)
		}
		orch.PublicHandleAgentGraphForScope(w, r, scope, id)
	case suffix == "graph/edge":
		orch.PublicHandleAgentGraphEdgeDeleteForScope(w, r, scope, id)
	case strings.HasPrefix(suffix, "graph/entity/"):
		sub := strings.TrimPrefix(suffix, "graph/entity/")
		switch {
		case strings.HasSuffix(sub, "/attr"):
			orch.PublicHandleAgentGraphAttrDeleteForScope(w, r, scope, id, strings.TrimSuffix(sub, "/attr"))
		case strings.HasSuffix(sub, "/alias"):
			orch.PublicHandleAgentGraphAliasDeleteForScope(w, r, scope, id, strings.TrimSuffix(sub, "/alias"))
		default:
			orch.PublicHandleAgentGraphEntityDeleteForScope(w, r, scope, id, sub)
		}
	case suffix == "inferred":
		orch.PublicHandleAgentInferredListForScope(w, r, scope, id)
	case strings.HasPrefix(suffix, "inferred/"):
		orch.PublicHandleAgentInferredDeleteForScope(w, r, scope, id, strings.TrimPrefix(suffix, "inferred/"))
	case suffix == "knowledge/auto-inferred":
		orch.PublicHandleAgentKnowledgeAutoInferredWipeForScope(w, r, scope, id)
	case suffix == "agent":
		orch.PublicHandleAgentRecordForScope(w, r, scope, id)
	default:
		http.NotFound(w, r)
	}
}
