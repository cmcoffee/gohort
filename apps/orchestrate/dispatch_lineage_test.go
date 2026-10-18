package orchestrate

// The limits an agents(run) chain is held to hold on every kind of hand-off:
// the lineage rides on the context, so delegate -> delegate counts, cannot
// loop, keeps the root's authority, and carries every guardrail above it.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEveryHandOffKeepsTheChainsLimits(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	save := func(rec AgentRecord) AgentRecord {
		rec.Owner, rec.OrchestratorPrompt = "u", "p"
		out, err := saveAgent(udb, rec)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	root := save(AgentRecord{Name: "Root", DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"b", "c"},
		Guardrails: "Never post anything publicly."})
	b := save(AgentRecord{ID: "b", Name: "B", Guardrails: "Never spend money."})
	c := save(AgentRecord{ID: "c", Name: "C"})
	d := save(AgentRecord{ID: "d", Name: "D"})
	own := save(AgentRecord{ID: "c-sub", Name: "C's helper", OwnedBy: "c"})

	// The first hop is the caller's own call, which its own gate judged.
	chain, origin, err := handOffLineage(context.Background(), udb, b, root.ID)
	if err != nil || len(chain) != 1 || origin == nil || origin.AgentID != root.ID {
		t.Fatalf("first hop: %v %v %v", chain, origin, err)
	}
	// One hand-off on, the root's allow-list still binds: D is not on it.
	ctx := withDispatchLineage(context.Background(), []string{root.ID, b.ID}, origin)
	if _, _, err := handOffLineage(ctx, udb, d, b.ID); err == nil || !strings.Contains(err.Error(), "started the chain") {
		t.Errorf("a delegate reached past the root's allow-list: %v", err)
	}
	if _, _, err := handOffLineage(ctx, udb, c, b.ID); err != nil {
		t.Errorf("a target the root allows was refused: %v", err)
	}
	// An agent's own sub-agent is its own to call, whatever the root allows.
	ctx = withDispatchLineage(context.Background(), []string{root.ID, b.ID, c.ID}, origin)
	if _, _, err := handOffLineage(ctx, udb, own, c.ID); err != nil {
		t.Errorf("a caller was refused its own sub-agent: %v", err)
	}
	// No agent twice on the path.
	if _, _, err := handOffLineage(ctx, udb, b, c.ID); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a loop back to B was allowed: %v", err)
	}
	// And no deeper than the cap.
	deep := withDispatchLineage(context.Background(), []string{root.ID, b.ID, c.ID, "x"}, origin)
	if _, _, err := handOffLineage(deep, udb, own, "x"); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Errorf("a chain past the depth limit ran: %v", err)
	}

	// Every agent above binds the run with its guardrails.
	got := inheritChainGuardrails(udb, []string{root.ID, b.ID}, c)
	if !strings.Contains(got.Guardrails, "publicly") || !strings.Contains(got.Guardrails, "spend money") {
		t.Errorf("guardrails from up the chain: %q", got.Guardrails)
	}

	// The lineage is what a hand-off from inside a run reads.
	stamped := withDispatchLineage(context.Background(), []string{root.ID, b.ID}, origin)
	if moved, _ := dispatchLineageFrom(carryDispatchLineage(stamped, context.Background())); len(moved) != 2 {
		t.Errorf("the lineage did not carry: %v", moved)
	}
}
