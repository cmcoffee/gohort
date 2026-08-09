// The audit exists because memory outlives what it names, and nothing
// reconciled the two: "pending task: get_top_stories with category=all" sat at
// the top of every prompt after the tool it named had stopped being callable.
//
// The hard part is not finding that note. It is not finding fifty other
// things, because a findings list that cries wolf is one people scroll past —
// so most of what follows pins what must stay SILENT.
package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
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
