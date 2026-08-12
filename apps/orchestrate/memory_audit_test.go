// The audit exists because memory outlives what it names, and nothing
// reconciled the two: "pending task: get_top_stories with category=all" sat at
// the top of every prompt after the tool it named had stopped being callable.
//
// The hard part is not finding that note. It is not finding fifty other
// things, because a findings list that cries wolf is one people scroll past —
// so most of what follows pins what must stay SILENT.
package orchestrate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

func auditFixture(t *testing.T) (*OrchestrateApp, Database, AgentRecord, string) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prev })
	const user = "craig@example.com"
	udb := UserDB(root, user)
	rec, err := saveAgent(udb, AgentRecord{
		Name: "Wren", Owner: user, OrchestratorPrompt: "p", EnableNotes: true,
	})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	return &OrchestrateApp{AppCore: AppCore{DB: root}}, udb, rec, user
}

func auditOf(t *testing.T, app *OrchestrateApp, udb Database, rec AgentRecord, user string) []MemoryFinding {
	t.Helper()
	return app.auditAgentMemory(udb, user, rec.ID, rec)
}

func kinds(findings []MemoryFinding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Kind)
	}
	return out
}

// The case that started it, caught on both counts: it is a parked call, and it
// names a tool that is only in the orphan pool.
func TestTheNoteThatStartedThisIsCaught(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "pending task: get_top_stories with category=all")
	AddOrphanedTempTools(udb, user, []OrphanedTempTool{{
		Tool: TempTool{Name: "get_top_stories"}, FormerAgentName: "Wren",
	}})

	found := auditOf(t, app, udb, rec, user)
	if len(found) == 0 {
		t.Fatal("the note that caused all this must be a finding")
	}
	var sawDead, sawParked bool
	for _, f := range found {
		if f.Layer != "Working notes" {
			t.Errorf("wrong layer: %+v", f)
		}
		switch f.Kind {
		case "dead_tool":
			sawDead = true
			if !strings.Contains(f.Detail, "Orphaned Tools") {
				t.Errorf("an orphaned tool finding must say where to re-home it: %s", f.Detail)
			}
		case "parked_call":
			sawParked = true
		}
	}
	if !sawDead || !sawParked {
		t.Errorf("want both a dead_tool and a parked_call finding, got %v", kinds(found))
	}
	// The one known-gone finding sorts above the shape judgement.
	if found[0].Kind != "dead_tool" {
		t.Errorf("known-dead should rank first, got %v", kinds(found))
	}
}

// A live tool named in a note is not a finding — the overwhelmingly common
// case, and the one that would make the audit useless if it fired.
func TestALiveToolIsNotAFinding(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	if err := AdminPersistTempTool(udb, user, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "user likes the news roundup from get_top_stories")

	if found := auditOf(t, app, udb, rec, user); len(found) != 0 {
		t.Errorf("a live tool must not be flagged: %+v", found)
	}
}

// Ordinary prose is full of snake_case. Naming one in passing is not a claim
// that a tool exists, so only call-shaped usage counts.
func TestOrdinaryProseIsNotAudited(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	SaveOperatingNotes(udb, factsNamespace(rec.ID), strings.Join([]string{
		"drafting section 3 of the guide",
		"the user's home_office setup has two monitors",
		"they prefer snake_case in code review",
		"waiting on the quarterly_report export to finish",
	}, "\n"))

	if found := auditOf(t, app, udb, rec, user); len(found) != 0 {
		t.Errorf("prose must not be audited as missing tools: %+v", found)
	}
}

// Call-shaped usage of a name that resolves nowhere IS worth surfacing.
func TestACallToANonexistentToolIsFlagged(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "to refresh the dashboard, call fetch_sales_totals every morning")

	found := auditOf(t, app, udb, rec, user)
	if len(found) != 1 || found[0].Kind != "dead_tool" {
		t.Fatalf("want one dead_tool finding, got %v", kinds(found))
	}
	if !strings.Contains(found[0].Detail, "fetch_sales_totals") {
		t.Errorf("the finding must name the tool: %s", found[0].Detail)
	}
}

// Notes are the current state of work. One nobody has rewritten in two months
// is describing something long finished, into every turn.
func TestStaleNotesAreFlaggedAndFreshOnesAreNot(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	ns := factsNamespace(rec.ID)

	SaveOperatingNotes(udb, ns, "mid-draft on section 3")
	if found := auditOf(t, app, udb, rec, user); len(found) != 0 {
		t.Fatalf("fresh notes are not a finding: %+v", found)
	}

	// Backdate the stored row rather than waiting two months.
	n := LoadOperatingNotes(udb, ns)
	n.UpdatedAt = time.Now().Add(-staleNotesAfter - 24*time.Hour)
	udb.Set(OperatingNotesTable, ns, n)

	found := auditOf(t, app, udb, rec, user)
	if len(found) != 1 || found[0].Kind != "stale_notes" {
		t.Fatalf("want one stale_notes finding, got %v", kinds(found))
	}
}

// A seed has never been rewritten by anyone. Calling it stale would be a
// complaint about configuration, not about memory.
func TestASeedNoteIsNeverCalledStale(t *testing.T) {
	app, udb, _, user := auditFixture(t)
	rec, err := saveAgent(udb, AgentRecord{
		Name: "Seeded", Owner: user, OrchestratorPrompt: "p",
		EnableNotes: true, SeedNotes: "start with the intake form",
	})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	for _, f := range auditOf(t, app, udb, rec, user) {
		if f.Kind == "stale_notes" {
			t.Errorf("a seed has no rewrite date to be stale against: %+v", f)
		}
	}
}

// Saved facts get audited too — a fact is replayed into every turn, so a task
// parked there is the same failure with a longer half-life.
func TestFactsAreAuditedAsWellAsNotes(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	ns := factsNamespace(rec.ID)
	StoreMemoryFact(udb, ns, "pending task: email the quarterly numbers", nil)

	found := auditOf(t, app, udb, rec, user)
	if len(found) == 0 {
		t.Fatal("a parked task in a fact must be flagged")
	}
	if found[0].Layer != "Saved facts" {
		t.Errorf("layer = %q, want Saved facts", found[0].Layer)
	}
}

// Clean memory produces nothing at all — the pane stays silent rather than
// showing an empty "Needs attention" box on every open.
func TestCleanMemoryHasNoFindings(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	SaveOperatingNotes(udb, factsNamespace(rec.ID), "drafting the release notes; user wants the terse version")
	StoreMemoryFact(udb, factsNamespace(rec.ID), "prefers concise replies", nil)

	if found := auditOf(t, app, udb, rec, user); len(found) != 0 {
		t.Errorf("clean memory must be silent: %+v", found)
	}
}

// Reference Memory is where "the working approach was tool X" gets recorded by
// memory_save, and then outlives X. Findings aggregate per tool: one retired
// tool can sit in dozens of saved entries, and thirty identical rows is how a
// findings list stops being read.
func TestReferenceMemoryIsAuditedAndAggregated(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	vdb := &DBase{Store: kvlite.MemStore()}
	prevV := VectorDB
	VectorDB = vdb
	t.Cleanup(func() { VectorDB = prevV })

	AddOrphanedTempTools(udb, user, []OrphanedTempTool{{Tool: TempTool{Name: "get_top_stories"}}})
	src := agentKnowledgePrefix(user, rec.ID)
	for i, text := range []string{
		"the reliable path for headlines is get_top_stories with category=all",
		"get_top_stories beats scraping the sites directly",
		"user prefers the terse summary format",
	} {
		vdb.Set(EmbeddedChunks, fmt.Sprintf("c%d", i), EmbeddedChunk{
			ID: fmt.Sprintf("c%d", i), Source: src, Text: text,
		})
	}

	found := auditOf(t, app, udb, rec, user)
	var ref []MemoryFinding
	for _, f := range found {
		if f.Layer == "Reference Memory" {
			ref = append(ref, f)
		}
	}
	if len(ref) != 1 {
		t.Fatalf("two chunks naming one dead tool must collapse to one finding, got %d: %+v", len(ref), ref)
	}
	if !strings.Contains(ref[0].Detail, "2 saved entries") {
		t.Errorf("the finding must carry the count: %s", ref[0].Detail)
	}
}

// Curated content is not derived memory — an uploaded document mentioning a
// retired tool is a document, not a belief the agent formed.
func TestCuratedChunksAreNotAudited(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	vdb := &DBase{Store: kvlite.MemStore()}
	prevV := VectorDB
	VectorDB = vdb
	t.Cleanup(func() { VectorDB = prevV })

	AddOrphanedTempTools(udb, user, []OrphanedTempTool{{Tool: TempTool{Name: "get_top_stories"}}})
	vdb.Set(EmbeddedChunks, "u1", EmbeddedChunk{
		ID: "u1", Source: agentKnowledgePrefix(user, rec.ID),
		ReportID: "orch-upload-1", Text: "the old runbook used get_top_stories",
	})

	for _, f := range auditOf(t, app, udb, rec, user) {
		if f.Layer == "Reference Memory" {
			t.Errorf("uploaded content is not derived memory: %+v", f)
		}
	}
}

// Another agent's chunks live in the same vector store under a different
// prefix and must not leak into this agent's findings.
func TestReferenceMemoryAuditIsScopedToTheAgent(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	vdb := &DBase{Store: kvlite.MemStore()}
	prevV := VectorDB
	VectorDB = vdb
	t.Cleanup(func() { VectorDB = prevV })

	AddOrphanedTempTools(udb, user, []OrphanedTempTool{{Tool: TempTool{Name: "get_top_stories"}}})
	vdb.Set(EmbeddedChunks, "other", EmbeddedChunk{
		ID: "other", Source: agentKnowledgePrefix(user, "some-other-agent"),
		Text: "call get_top_stories for the feed",
	})

	for _, f := range auditOf(t, app, udb, rec, user) {
		if f.Layer == "Reference Memory" {
			t.Errorf("another agent's memory must not appear here: %+v", f)
		}
	}
}

// Graph attributes are where a tool name realistically lands in the graph.
// The finding has to name the entity — "Graph Memory" alone doesn't say which
// of thirty nodes to open.
func TestGraphAttributesAreAuditedAndNameTheEntity(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	AddOrphanedTempTools(udb, user, []OrphanedTempTool{{Tool: TempTool{Name: "get_top_stories"}}})
	ns := factsNamespace(rec.ID)
	udb.Set(GraphEntityTable, ns+"/thing:morning-brief", GraphEntity{
		Namespace: ns, ID: "thing:morning-brief", Kind: "thing", Name: "Morning Brief",
		Attrs: map[string]string{"built_from": "get_top_stories"},
	})

	var graph []MemoryFinding
	for _, f := range auditOf(t, app, udb, rec, user) {
		if f.Layer == "Graph Memory" {
			graph = append(graph, f)
		}
	}
	if len(graph) != 1 {
		t.Fatalf("want one Graph Memory finding, got %d: %+v", len(graph), graph)
	}
	if !strings.Contains(graph[0].Detail, "Morning Brief") {
		t.Errorf("the finding must name the entity: %s", graph[0].Detail)
	}
}

// Attribute KEYS are snake_case by convention. Auditing them would flag most
// of the graph on the first open.
func TestGraphAttributeKeysAreNotAudited(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	ns := factsNamespace(rec.ID)
	udb.Set(GraphEntityTable, ns+"/person:dana", GraphEntity{
		Namespace: ns, ID: "person:dana", Kind: "person", Name: "Dana",
		Attrs: map[string]string{"home_office": "two monitors", "preferred_contact": "text"},
	})

	if found := auditOf(t, app, udb, rec, user); len(found) != 0 {
		t.Errorf("attribute keys must not be audited as tools: %+v", found)
	}
}

// The audit only runs when someone opens the Memory pane — the same hole the
// original failure fell through, since the note survived because nobody went
// looking. Deleting an agent is the moment the framework KNOWS a capability
// just disappeared, so that is where it says who was still counting on it.
func TestDeletingAnAgentFlagsWhoStillRemembersItsTool(t *testing.T) {
	app, udb, keeper, user := auditFixture(t)
	_ = app
	// The agent that carries the tool, and a second one that remembers it.
	carrier, err := saveAgent(udb, AgentRecord{Name: "Carrier", Owner: user, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := AdminPersistTempTool(udb, user, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	SetUserToolScopeAgents(udb, user, "get_top_stories", []string{carrier.ID})
	SaveOperatingNotes(udb, factsNamespace(keeper.ID), "headlines come from get_top_stories")

	if err := deleteAgent(udb, carrier.ID, user); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var found *Authorization
	for _, a := range ListAuthorizations(RootDB, user) {
		if a.Action == orphanMemoryRefAction && a.Agent == keeper.ID {
			cp := a
			found = &cp
		}
	}
	if found == nil {
		t.Fatal("the agent that remembers the tool must be flagged when it goes dark")
	}
	if found.Brief != "get_top_stories" {
		t.Errorf("brief = %q, want the tool name", found.Brief)
	}
	// Nothing is blocked on this — the agent runs fine, it just runs believing
	// something untrue — so it belongs with the OFFERS rather than in the count
	// of things the user is blocking. That classification lives in
	// approvalIsSuggestion, which is not asserted here because it is not part
	// of this commit.
}

// Only agents that actually reference the tool get flagged. Queuing a card for
// every agent on every delete would make the pane worthless.
func TestAgentsThatNeverMentionedTheToolAreNotFlagged(t *testing.T) {
	app, udb, bystander, user := auditFixture(t)
	_ = app
	carrier, err := saveAgent(udb, AgentRecord{Name: "Carrier", Owner: user, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := AdminPersistTempTool(udb, user, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	SetUserToolScopeAgents(udb, user, "get_top_stories", []string{carrier.ID})
	SaveOperatingNotes(udb, factsNamespace(bystander.ID), "drafting section 3")

	if err := deleteAgent(udb, carrier.ID, user); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, a := range ListAuthorizations(RootDB, user) {
		if a.Action == orphanMemoryRefAction {
			t.Errorf("nobody referenced the tool; nothing should be queued: %+v", a)
		}
	}
}

// A delete that orphans nothing says nothing.
func TestADeleteThatOrphansNothingIsSilent(t *testing.T) {
	app, udb, keeper, user := auditFixture(t)
	_ = app
	other, err := saveAgent(udb, AgentRecord{Name: "Other", Owner: user, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	// Shared user-wide, so deleting an agent does not orphan it.
	if err := AdminPersistTempTool(udb, user, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	SaveOperatingNotes(udb, factsNamespace(keeper.ID), "headlines come from get_top_stories")

	if err := deleteAgent(udb, other.ID, user); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, a := range ListAuthorizations(RootDB, user) {
		if a.Action == orphanMemoryRefAction {
			t.Errorf("the tool is still callable; nothing to flag: %+v", a)
		}
	}
}

// Facts and graph attributes count as remembering it, not just notes.
func TestFactsAndGraphAlsoCountAsRemembering(t *testing.T) {
	_, udb, _, _ := auditFixture(t)
	ns := "probe"
	StoreMemoryFact(udb, ns, "the feed comes from get_top_stories", nil)
	if !memoryMentionsTool(udb, ns, "get_top_stories") {
		t.Error("a fact naming the tool counts")
	}
	if memoryMentionsTool(udb, ns, "some_other_tool") {
		t.Error("an unrelated name must not match")
	}
	// Substring matches would flag get_top_stories_v2 as get_top_stories.
	StoreMemoryFact(udb, "probe2", "we use get_top_stories_v2 now", nil)
	if memoryMentionsTool(udb, "probe2", "get_top_stories") {
		t.Error("must match whole names, not substrings")
	}
}

// TestSystemScopedMemoryIsNotAuditedForToolNames — the audit's premise does not
// hold everywhere it runs.
//
// The dead-tool scan reads every snake_case identifier as a tool name and
// checks it against the reader's pool. For an agent's own memory that is right.
// For an app agent working a per-system scope the memory describes a MACHINE,
// where service names, config keys, unit files and package names are all
// snake_case — so servitor's notes ("run systemctl_status", "check
// max_connections") came back as a list of tools that no longer exist, under a
// heading saying "Needs attention". The audit was confidently reporting
// correctly-recorded system facts as broken references.
func TestSystemScopedMemoryIsNotAuditedForToolNames(t *testing.T) {
	app, udb, _, user := auditFixture(t)
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-audit-probe", OwningApp: "Test", Name: "Probe", Prompt: "p", Hidden: true,
	})
	rec := AgentRecord{ID: "app-audit-probe", Name: "Probe", Owner: seedOwner,
		OrchestratorPrompt: "p", EnableNotes: true}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	// Prose about a SYSTEM, phrased the way an investigator records it.
	SaveOperatingNotes(udb, factsNamespace(rec.ID),
		"To check the pool, run max_connections and read log_rotate; call systemctl_status(nginx) after.")

	for _, f := range auditOf(t, app, udb, rec, user) {
		if f.Kind == "dead_tool" {
			t.Errorf("a system fact was reported as a missing tool: %q — %s", f.Quote, f.Detail)
		}
	}
}

// TestAnOrdinaryAgentIsStillAuditedForToolNames — the mirror. Suppressing the
// scan everywhere would have been the easy fix and would have thrown away the
// finding the feature exists for.
func TestAnOrdinaryAgentIsStillAuditedForToolNames(t *testing.T) {
	app, udb, rec, user := auditFixture(t)
	SaveOperatingNotes(udb, factsNamespace(rec.ID),
		"Remember to call fetch_quarterly_report() each Monday.")

	var sawDead bool
	for _, f := range auditOf(t, app, udb, rec, user) {
		if f.Kind == "dead_tool" {
			sawDead = true
		}
	}
	if !sawDead {
		t.Error("an agent's own note naming a tool that does not exist was not flagged — " +
			"the scope gate went too wide and the audit now finds nothing anywhere")
	}
}
