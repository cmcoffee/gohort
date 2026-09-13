package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func spendTestRoot(t *testing.T) {
	t.Helper()
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
}

// Runs bank into a daily row per (owner, agent); the pane sums them into
// windows, names the agent, and orders by 30-day spend. Zero usage banks
// nothing; a sub-agent's scoped tracker banks to the sub-agent.
func TestAgentSpendBanksAndAggregates(t *testing.T) {
	spendTestRoot(t)
	digest := AgentRecord{ID: "a-digest", Name: "Nightly digest", Owner: "alice"}
	helper := AgentRecord{ID: "a-helper", Name: "Helper", Owner: "alice"}

	bankAgentSpend("alice", digest, UsageDiff{LeadInput: 1000, LeadOutput: 200})
	bankAgentSpend("alice", digest, UsageDiff{WorkerInput: 500})
	bankAgentSpend("alice", helper, UsageDiff{WorkerInput: 10})
	bankAgentSpend("alice", helper, UsageDiff{}) // nothing to bank
	bankAgentSpend("bob", helper, UsageDiff{WorkerInput: 99999})

	// A scoped tracker on a context banks what IT saw.
	ctx, tracker := WithRequestUsage(context.Background())
	tracker.AddWorkerTokens(40, 2, 0, 0)
	bankScopedSpend(ctx, "alice", helper)
	bankScopedSpend(context.Background(), "alice", helper) // no tracker: no-op

	rows := agentSpendRows("alice", time.Now(), time.UTC)
	if len(rows) != 2 || rows[0].ID != "a-digest" || rows[0].Agent != "Nightly digest" {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.Contains(rows[0].Today, "1,700 tokens") && !strings.Contains(rows[0].Today, "1700 tokens") {
		t.Fatalf("digest today = %q", rows[0].Today)
	}
	if rows[0].Runs != "2 run(s)" || rows[1].Runs != "2 run(s)" {
		t.Fatalf("runs = %q / %q", rows[0].Runs, rows[1].Runs)
	}
	if rows[1].Today != rows[1].Week || rows[1].Week != rows[1].Month {
		t.Fatalf("one day of use should read the same in every window: %+v", rows[1])
	}
	if rows[0].Last == "" {
		t.Fatal("last active missing")
	}
	// Bob's rows are bob's.
	if bob := agentSpendRows("bob", time.Now(), time.UTC); len(bob) != 1 || bob[0].ID != "a-helper" {
		t.Fatalf("bob rows = %+v", bob)
	}
}

// Spend accrues to the agent's author; a seed's runtime user; an unowned
// record's runtime user.
func TestSpendOwner(t *testing.T) {
	if spendOwner(AgentRecord{Owner: "alice"}, "bob") != "alice" {
		t.Fatal("author should own the spend")
	}
	if spendOwner(AgentRecord{Owner: seedOwner}, "bob") != "bob" {
		t.Fatal("a seed's spend belongs to whoever ran it")
	}
	if spendOwner(AgentRecord{}, "bob") != "bob" {
		t.Fatal("no owner → runtime user")
	}
}

// Rows past the keep window are dropped on a new day's first bank.
func TestAgentSpendPrunesOldDays(t *testing.T) {
	spendTestRoot(t)
	old := time.Now().UTC().AddDate(0, 0, -agentSpendKeepDay-5).Format("2006-01-02")
	RootDB.Set(agentSpendTable, agentSpendKey("alice", "a-1", old), agentSpendDay{Owner: "alice", AgentID: "a-1", Day: old, Usage: UsageDiff{LeadInput: 5}})
	bankAgentSpend("alice", AgentRecord{ID: "a-1", Name: "One"}, UsageDiff{LeadInput: 1})
	var gone agentSpendDay
	if RootDB.Get(agentSpendTable, agentSpendKey("alice", "a-1", old), &gone) {
		t.Fatal("old row should be pruned")
	}
	if rows := agentSpendRows("alice", time.Now(), time.UTC); len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}
