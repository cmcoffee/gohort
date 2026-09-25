package orchestrate

// Ownership carries approvals DOWN and, since delegated_guardrails, restrictions
// too. The tool surface is the one thing that travels neither way: a sub-agent's
// AllowedTools is unioned with the parent's inheritable catalog and never
// compared to it.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func ceilingFixture(t *testing.T) (Database, string) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	return UserDB(db, "alice"), "alice"
}

func mkAgent(t *testing.T, udb Database, name, ownedBy string, tools []string) AgentRecord {
	t.Helper()
	return mkAgentRec(t, udb, AgentRecord{
		Name: name, Owner: "alice", Description: "d",
		OrchestratorPrompt: "p", OwnedBy: ownedBy, AllowedTools: tools,
	})
}

func mkAgentRec(t *testing.T, udb Database, a AgentRecord) AgentRecord {
	t.Helper()
	rec, err := saveAgent(udb, a)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestASubAgentInsideItsParentIsNotReported(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", []string{"web_search", "fetch_url"})
	mkAgent(t, udb, "Child", parent.ID, []string{"fetch_url"})

	if got := SubAgentToolExceptions(udb, user); len(got) != 0 {
		t.Errorf("a child within its parent's surface is not an exception: %+v", got)
	}
}

// The escalation this exists to find.
func TestASubAgentReachingPastItsParentIsReported(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", []string{"fetch_url"})
	mkAgent(t, udb, "Child", parent.ID, []string{"fetch_url", "web_search", "send_email"})

	got := SubAgentToolExceptions(udb, user)
	if len(got) != 1 {
		t.Fatalf("want one exception, got %+v", got)
	}
	if len(got[0].Tools) != 2 || got[0].Tools[0] != "send_email" || got[0].Tools[1] != "web_search" {
		t.Errorf("should name exactly what the parent cannot reach, sorted: %v", got[0].Tools)
	}
	if got[0].ParentName != "Parent" {
		t.Errorf("should name the parent: %q", got[0].ParentName)
	}
}

// The loudest shape gets its own field rather than a list of the whole pool.
func TestAnUnrestrictedChildUnderACuratedParent(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", []string{"fetch_url"})
	mkAgent(t, udb, "Child", parent.ID, nil) // no allowlist = the whole pool

	got := SubAgentToolExceptions(udb, user)
	if len(got) != 1 || !got[0].Unrestricted {
		t.Fatalf("an unnarrowed child under a curated parent must be flagged: %+v", got)
	}
	if len(got[0].Tools) != 0 {
		t.Errorf("listing the entire pool would bury the point: %v", got[0].Tools)
	}
}

// An unrestricted PARENT reaches everything, so nothing a child names is beyond
// it — reporting there would be noise on the commonest configuration.
func TestAnUnrestrictedParentYieldsNoExceptions(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", nil)
	mkAgent(t, udb, "Child", parent.ID, []string{"web_search", "send_email"})

	if got := SubAgentToolExceptions(udb, user); len(got) != 0 {
		t.Errorf("a child cannot exceed a parent that already reaches everything: %+v", got)
	}
}

// Always-on tools are granted to every agent whatever its allowlist says, so
// they never count as reaching past anything.
func TestAlwaysOnToolsAreNotExceptions(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", []string{"fetch_url"})
	mkAgent(t, udb, "Child", parent.ID, append([]string{"fetch_url", "workspace"}, frameworkUtilityTools...))

	if got := SubAgentToolExceptions(udb, user); len(got) != 0 {
		t.Errorf("always-on tools are not an escalation: %+v", got)
	}
}

// A child explicitly narrowed to nothing is the safest state, not an exception.
func TestAChildWithTheNoToolsSentinelIsNotReported(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", []string{"fetch_url"})
	mkAgent(t, udb, "Child", parent.ID, []string{noToolsSentinel})

	if got := SubAgentToolExceptions(udb, user); len(got) != 0 {
		t.Errorf("a child with no tools cannot exceed anything: %+v", got)
	}
}

// A dangling OwnedBy never reaches the report: listAgents promotes an orphan
// to top-level and persists it, because a sub-agent is pinned Hidden and an
// orphan would be invisible and unmanageable. Asserted so the report is not
// later grown a branch for a state that cannot arrive.
func TestAnOrphanedSubAgentIsHealedNotReported(t *testing.T) {
	udb, user := ceilingFixture(t)
	mkAgent(t, udb, "Child", "no-such-parent", []string{"fetch_url"})

	if got := SubAgentToolExceptions(udb, user); len(got) != 0 {
		t.Errorf("an orphan is promoted to top-level by the loader, not reported here: %+v", got)
	}
	for _, a := range listAgents(udb, user) {
		if a.Name == "Child" && a.OwnedBy != "" {
			t.Errorf("the loader should have cleared the dangling parent, got %q", a.OwnedBy)
		}
	}
}

// Top-level agents are not sub-agents and are never compared to anything.
func TestTopLevelAgentsAreIgnored(t *testing.T) {
	udb, user := ceilingFixture(t)
	mkAgent(t, udb, "Standalone", "", []string{"web_search", "send_email"})

	if got := SubAgentToolExceptions(udb, user); len(got) != 0 {
		t.Errorf("an agent with no parent has no ceiling: %+v", got)
	}
}

// A flag, not a tool name — and the case the allowlist comparison cannot see.
// Fleet mints delegate / message_contact / notify_me / standing-agent and
// monitor management at dispatch, and enforceSubAgentPosture does not pin it.
func TestAChildHoldingFleetItsParentLacksIsReported(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", []string{"fetch_url"})
	mkAgentRec(t, udb, AgentRecord{
		Name: "Child", Owner: "alice", Description: "d", OrchestratorPrompt: "p",
		OwnedBy: parent.ID, AllowedTools: []string{"fetch_url"}, Fleet: true,
	})

	got := SubAgentToolExceptions(udb, user)
	if len(got) != 1 {
		t.Fatalf("want one exception, got %+v", got)
	}
	if len(got[0].Tools) != 0 {
		t.Errorf("its tool list sits inside the parent's; only the flag exceeds: %v", got[0].Tools)
	}
	if len(got[0].Capabilities) != 1 || !strings.Contains(got[0].Capabilities[0], "Conductor") {
		t.Errorf("should name the capability: %v", got[0].Capabilities)
	}
}

// A parent that HAS the flag confers nothing to report.
func TestAChildSharingItsParentsFlagsIsNotReported(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgentRec(t, udb, AgentRecord{
		Name: "Parent", Owner: "alice", Description: "d", OrchestratorPrompt: "p",
		AllowedTools: []string{"fetch_url"}, Fleet: true, Author: true,
	})
	mkAgentRec(t, udb, AgentRecord{
		Name: "Child", Owner: "alice", Description: "d", OrchestratorPrompt: "p",
		OwnedBy: parent.ID, AllowedTools: []string{"fetch_url"}, Fleet: true, Author: true,
	})

	if got := SubAgentToolExceptions(udb, user); len(got) != 0 {
		t.Errorf("a child matching its parent's capabilities is not an exception: %+v", got)
	}
}

// An unbounded PARENT reaches every tool, and still confers no flag it does not
// hold — a pool is tools, not capabilities.
func TestAnUnrestrictedParentStillDoesNotConferItsFlags(t *testing.T) {
	udb, user := ceilingFixture(t)
	parent := mkAgent(t, udb, "Parent", "", nil) // no allowlist, no Fleet
	mkAgentRec(t, udb, AgentRecord{
		Name: "Child", Owner: "alice", Description: "d", OrchestratorPrompt: "p",
		OwnedBy: parent.ID, AllowedTools: []string{"fetch_url"}, Fleet: true,
	})

	got := SubAgentToolExceptions(udb, user)
	if len(got) != 1 || len(got[0].Capabilities) != 1 {
		t.Fatalf("the flag still exceeds a parent that merely reaches every tool: %+v", got)
	}
}
