package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// AgentScope instantiates an agent TEMPLATE against a caller-supplied state
// namespace. The agent's persona / prompt / tools come from the template
// (TemplateOwner + AgentID — the single source of truth, edited in one place);
// its sessions, Explicit Memory, and Knowledge/RAG live under ScopeUser — a
// synthetic runtime identity (e.g. "app:servitor:appliance-123") isolated from
// the template owner's own state. Two scopes never share any state.
//
// This is the app-facing seam for "use an orchestrate agent as a reusable
// template with LOCAL state". It's the same mechanism phantom already uses to run
// an agent under "phantom:<contact>" — formalized so any app can adopt it (e.g.
// to run a per-appliance lead/worker pair off shared agent definitions). The
// underlying run goes through RunAgentSyncContinuingRich, which already resolves
// config (AgentOwner) and state (RuntimeUser) from separate stores.
type AgentScope struct {
	// AgentID is the template agent's id (or name). For an app-registered agent
	// (appagents.RegisterAppAgent) leave TemplateOwner blank — app + built-in
	// agents resolve from the seed store.
	AgentID string
	// ScopeUser is the synthetic runtime identity = the state namespace. REQUIRED.
	// Use a stable, app-prefixed string, e.g. "app:<app>:<instance>", so an app's
	// instances can't collide with each other or with a real user's email-keyed
	// store. (Avoid "/", "\\", ".." — it also names a workspace dir.)
	ScopeUser string
	// TemplateOwner is the owner whose store holds the template AgentRecord. Blank
	// ⇒ the seed store ("system"), where app-registered + built-in agents live.
	// Set it to run a specific user's own agent as a template.
	TemplateOwner string
	// SessionID names the conversation thread WITHIN the scope. Blank ⇒ a
	// deterministic per-(scope, agent) id — one continuing thread.
	SessionID string
}

// RunScopedAgent runs the scope's template agent on one message and returns its
// reply (+ any attachments it produced). A thin, intention-revealing wrapper over
// RunAgentSyncContinuingRich: config from the template, state under ScopeUser.
// status (optional) receives mid-turn progress pings (a lead reporting to its
// caller). Tool calls auto-approve, matching the dispatch path — the app is
// acting on a user's behalf, not autonomously.
// appTools (optional, variadic) are per-instance closure tools injected into this
// run only — e.g. an app's appliance-bound SSH command tools. They ride alongside
// the template's own allowed tools.
func (T *OrchestrateApp) RunScopedAgent(ctx context.Context, scope AgentScope, message string, status func(string), appTools ...AgentToolDef) (AgentSyncResult, error) {
	if scope.ScopeUser == "" {
		return AgentSyncResult{}, errors.New("scope.ScopeUser is required (the local state namespace)")
	}
	if scope.AgentID == "" {
		return AgentSyncResult{}, errors.New("scope.AgentID is required (the template agent)")
	}
	owner := scope.TemplateOwner
	if owner == "" {
		owner = seedOwner // app-registered + built-in agents live in the seed store
	}
	return T.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
		AgentOwner:     owner,
		RuntimeUser:    scope.ScopeUser,
		AgentKey:       scope.AgentID,
		SubSessionID:   scope.SessionID,
		Message:        message,
		StatusCallback: status,
		AppTools:       appTools,
	})
}

// RunScopedAgentRich is the full-control scoped entry: the caller supplies a
// complete AgentSyncRun (Message, FreshSession, SystemPromptOverride, AppTools,
// Loop, InjectionQueueID, StatusCallback, …) and the scope fills in the identity
// fields (owner from the template store, runtime user + agent key from the
// scope). For sophisticated instances — servitor's investigator — that need the
// loop knobs and their own per-run prompt while still running in the scope.
func (T *OrchestrateApp) RunScopedAgentRich(ctx context.Context, scope AgentScope, run AgentSyncRun) (AgentSyncResult, error) {
	if scope.ScopeUser == "" {
		return AgentSyncResult{}, errors.New("scope.ScopeUser is required (the local state namespace)")
	}
	if scope.AgentID == "" {
		return AgentSyncResult{}, errors.New("scope.AgentID is required (the template agent)")
	}
	owner := scope.TemplateOwner
	if owner == "" {
		owner = seedOwner
	}
	run.AgentOwner = owner
	run.RuntimeUser = scope.ScopeUser
	run.AgentKey = scope.AgentID
	if run.SubSessionID == "" {
		run.SubSessionID = scope.SessionID
	}
	return T.RunAgentSyncContinuingRich(ctx, run)
}

// SeedScopedMemory writes explicit-memory facts into a scope's namespace so the
// scoped agent recalls them on its next run — the app injecting context an
// instance should already "know" (e.g. an appliance's known facts). Dedup +
// supersession run via the worker model, exactly like the agent's own store_fact
// tool, so re-seeding the same fact is idempotent. Lands in the SAME namespace
// the scoped run reads, so it's immediately retrievable.
func (T *OrchestrateApp) SeedScopedMemory(scope AgentScope, notes ...string) error {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return errors.New("scope.ScopeUser and scope.AgentID are required")
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return fmt.Errorf("no store for scope %q", scope.ScopeUser)
	}
	ns := factsNamespace(scope.AgentID)
	for _, note := range notes {
		if strings.TrimSpace(note) == "" {
			continue
		}
		StoreMemoryFact(db, ns, note, T.WorkerChat)
	}
	return nil
}

// SeedScopedGraphEntity writes (or MERGES) one graph-memory entity into a
// scope's namespace — the keyed/structured counterpart to SeedScopedMemory. An
// app uses this to seed an instance's known STRUCTURED facts as entity
// attributes (e.g. an appliance's os/kernel/port), which is the faithful home
// for key→value data: deterministic overwrite by attr key, no LLM cost, and it
// can grow relationships (edges) later. UpsertGraphEntity merges by name/alias,
// so re-seeding the same entity updates its attrs in place rather than
// duplicating. Lands in the SAME namespace (factsNamespace) the scoped run's
// graph tools read, so recall_about / the Memory modal see it immediately.
func (T *OrchestrateApp) SeedScopedGraphEntity(scope AgentScope, kind, name string, aliases []string, attrs map[string]string) error {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return errors.New("scope.ScopeUser and scope.AgentID are required")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("entity name is required")
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return fmt.Errorf("no store for scope %q", scope.ScopeUser)
	}
	e, _ := UpsertGraphEntity(db, factsNamespace(scope.AgentID), kind, name, aliases, attrs)
	if e.ID == "" {
		return fmt.Errorf("failed to record entity %q", name)
	}
	return nil
}

// SeedScopedGraphLink records a RELATIONSHIP in a scope's graph: it upserts the
// subject (with optional attrs) and object entities and links a subject→relation
// →object edge. This is how an app builds a real multi-entity graph (services,
// configs, dependencies) rather than piling everything onto one node via
// SeedScopedGraphEntity. Entities auto-merge by name/alias; replace=true drops a
// prior single-valued relation for this subject+relation first.
func (T *OrchestrateApp) SeedScopedGraphLink(scope AgentScope, subjectKind, subject string, subjectAttrs map[string]string, relation, objectKind, object, note string, replace bool) error {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return errors.New("scope.ScopeUser and scope.AgentID are required")
	}
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(relation) == "" || strings.TrimSpace(object) == "" {
		return errors.New("subject, relation, and object are required")
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return fmt.Errorf("no store for scope %q", scope.ScopeUser)
	}
	ns := factsNamespace(scope.AgentID)
	subj, _ := UpsertGraphEntity(db, ns, subjectKind, subject, nil, subjectAttrs)
	obj, _ := UpsertGraphEntity(db, ns, objectKind, object, nil, nil)
	if subj.ID == "" || obj.ID == "" {
		return errors.New("failed to record entities")
	}
	LinkGraphEdge(db, ns, subj.ID, relation, obj.ID, note, replace)
	return nil
}

// ScopedGraphEntity looks up one graph entity (by name or alias) in a scope's
// namespace — the read counterpart to SeedScopedGraphEntity. An app reading the
// structured facts it recorded as entity attrs (servitor reading an appliance's
// facts for its prompt) uses this.
func (T *OrchestrateApp) ScopedGraphEntity(scope AgentScope, nameOrAlias string) (GraphEntity, bool) {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return GraphEntity{}, false
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return GraphEntity{}, false
	}
	return FindGraphEntity(db, factsNamespace(scope.AgentID), nameOrAlias)
}

// ScopedGraphSummary renders the scope's ENTIRE graph — every entity with its
// attrs and outgoing relationships — as a compact text block for injection into
// a prompt. It's the read-back counterpart to link_entities: an agent with no
// graph-traverse tool still sees the topology it recorded. Empty when the graph
// is empty. Rendering is generic (no domain terms).
func (T *OrchestrateApp) ScopedGraphSummary(scope AgentScope) string {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return ""
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return ""
	}
	ns := factsNamespace(scope.AgentID)
	ents := ListGraphEntities(db, ns)
	if len(ents) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range ents {
		b.WriteString("- ")
		b.WriteString(e.Name)
		if e.Kind != "" && e.Kind != "thing" {
			b.WriteString(" (" + e.Kind + ")")
		}
		if len(e.Attrs) > 0 {
			keys := make([]string, 0, len(e.Attrs))
			for k := range e.Attrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, k+"="+e.Attrs[k])
			}
			b.WriteString(": " + strings.Join(parts, ", "))
		}
		b.WriteString("\n")
		for _, ed := range GraphEdgesFrom(db, ns, e.ID) {
			rel := strings.ReplaceAll(ed.Rel, "_", " ")
			b.WriteString("    → " + rel + " " + graphEndName(db, ns, ed.To))
			if ed.Note != "" {
				b.WriteString(" (" + ed.Note + ")")
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// WipeScopedMemory clears EVERY layer of a scope's memory — Explicit facts,
// Graph entities (and their edges), and Reference Memory (derived chunks) — for
// a full per-instance reset (e.g. servitor's "Clear Memory"). The counterpart
// to the Seed* helpers. Best-effort per layer; superseded fact tombstones (never
// surfaced) and any curated uploads are intentionally left untouched.
func (T *OrchestrateApp) WipeScopedMemory(scope AgentScope) error {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return errors.New("scope.ScopeUser and scope.AgentID are required")
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return fmt.Errorf("no store for scope %q", scope.ScopeUser)
	}
	ns := factsNamespace(scope.AgentID)
	for _, f := range ListMemoryFacts(db, ns) {
		ForgetMemoryFactByID(db, ns, f.ID)
	}
	for _, e := range ListGraphEntities(db, ns) {
		DeleteGraphEntity(db, ns, e.ID) // removes the entity and its edges
	}
	// Reference Memory: derived chunks (orch-know-*) under this scope's corpus.
	if VectorDB != nil {
		prefix := agentKnowledgePrefix(scope.ScopeUser, scope.AgentID)
		for _, key := range VectorDB.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !VectorDB.Get(EmbeddedChunks, key, &c) {
				continue
			}
			if strings.HasPrefix(c.Source, prefix) && strings.HasPrefix(c.ReportID, "orch-know-") {
				VectorDB.Unset(EmbeddedChunks, key)
			}
		}
		invalidateChunkCacheIfPossible()
	}
	return nil
}

// --- per-instance rules overlay (enabler #2 of the layered model) ------------

const agentScopeRulesTable = "agent_scope_rules"

// scopeRulesRecord wraps the per-scope rule list (struct, not a bare slice, to
// keep the kvlite/gob value type stable if the shape ever grows).
type scopeRulesRecord struct {
	Rules []string `json:"rules"`
}

// listScopeRules returns an instance's own rules (the overlay layered OVER the
// template's record rules at run time).
func listScopeRules(udb Database, agentID string) []string {
	if udb == nil || agentID == "" {
		return nil
	}
	var rec scopeRulesRecord
	udb.Get(agentScopeRulesTable, agentID, &rec)
	return rec.Rules
}

// mergeScopeRules appends an instance's rules under the template's base rules.
// renderRulesPromptSection renders the combined string, so a plain concat with a
// clear marker is enough — base first, then the per-instance additions.
func mergeScopeRules(base string, overlay []string) string {
	if len(overlay) == 0 {
		return base
	}
	add := strings.Join(overlay, "\n")
	if strings.TrimSpace(base) == "" {
		return add
	}
	return base + "\n" + add
}

// SeedScopedRules adds per-instance rules to a scope (deduped, idempotent) — the
// overlay that layers over the template's base rules. e.g. servitor recording an
// operator's per-appliance directive ("this box is prod — read-only only").
func (T *OrchestrateApp) SeedScopedRules(scope AgentScope, rules ...string) error {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return errors.New("scope.ScopeUser and scope.AgentID are required")
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return fmt.Errorf("no store for scope %q", scope.ScopeUser)
	}
	var rec scopeRulesRecord
	db.Get(agentScopeRulesTable, scope.AgentID, &rec)
	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		dup := false
		for _, ex := range rec.Rules {
			if ex == r {
				dup = true
				break
			}
		}
		if !dup {
			rec.Rules = append(rec.Rules, r)
		}
	}
	db.Set(agentScopeRulesTable, scope.AgentID, rec)
	return nil
}

// SeedScopedKnowledge ingests a document into a scope's Knowledge/RAG store so the
// scoped agent's search_knowledge tool retrieves it on later runs. topic/title
// label the chunk (topic groups related docs; title is the source name). Blocks
// until embedded, so the knowledge is ready before the next run. Lands under the
// SAME (ScopeUser, AgentID) source tag the scoped run searches.
func (T *OrchestrateApp) SeedScopedKnowledge(ctx context.Context, scope AgentScope, topic, title, body string) error {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return errors.New("scope.ScopeUser and scope.AgentID are required")
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("body is required")
	}
	ingestAgentKnowledge(ctx, T.DB, scope.ScopeUser, scope.AgentID, topic, title, body)
	return nil
}
