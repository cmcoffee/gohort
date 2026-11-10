package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// preguardWorld is a fresh switch and record, and a System One that settles
// the rules named in notEngaged.
func preguardWorld(t *testing.T, notEngaged ...string) *[]string {
	t.Helper()
	saved, prev := RootDB, QuickDecide
	RootDB = &DBase{Store: kvlite.MemStore()}
	asked := &[]string{}
	QuickDecide = func(ctx context.Context, q QuickChoice) (string, bool) {
		*asked = append(*asked, q.State)
		for _, r := range notEngaged {
			if strings.Contains(q.State, "RULE: "+r+"\n") {
				return "not_engaged", true
			}
		}
		return "", false
	}
	t.Cleanup(func() { RootDB, QuickDecide = saved, prev })
	return asked
}

// Off, or without a turn to note its skips in (a dry run, an appeal), the
// pre-guard asks nothing and keeps every rule. On, it takes out the agent's
// own rules System One settles as not engaged, never the deployment's,
// notes each skip in the turn's trail, and counts them.
func TestThePreguardSkipsOnlyWhatSystemOneSettles(t *testing.T) {
	asked := preguardWorld(t, "never mention salary", "no jokes")
	app := &OrchestrateApp{}
	rules := []guardrailRule{{Text: "never mention salary"}, {Text: "no jokes", Global: true}, {Text: "cite sources"}}
	var notes []string
	ctx := withPreguardNotes(context.Background(), func(kind, detail string) { notes = append(notes, kind+": "+detail) })

	if kept, skipped := app.preguard(ctx, AgentRecord{}, guardHookPreOutput, "It is sunny.", rules); len(kept) != 3 || skipped != nil || len(*asked) != 0 {
		t.Fatalf("off: kept %d, skipped %d, asked %d", len(kept), len(skipped), len(*asked))
	}
	UpdatePreguard(func(r *PreguardRecord) { r.On = true })
	if kept, _ := app.preguard(context.Background(), AgentRecord{}, guardHookPreOutput, "It is sunny.", rules); len(kept) != 3 || len(*asked) != 0 {
		t.Fatal("a check with no turn to note in was pre-guarded")
	}
	kept, skipped := app.preguard(ctx, AgentRecord{}, guardHookPreOutput, "It is sunny.", rules)
	if len(skipped) != 1 || skipped[0].Text != "never mention salary" || len(kept) != 2 {
		t.Errorf("kept %+v, skipped %+v", kept, skipped)
	}
	if len(*asked) != 2 {
		t.Errorf("System One was asked %d times; the deployment's rule must not be asked at all", len(*asked))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "guardrail-preguard") || !strings.Contains(notes[0], "never mention salary") {
		t.Errorf("trail: %v", notes)
	}
	if r := LoadPreguard(); r.Skipped != 1 {
		t.Errorf("record: %+v", r)
	}
}

// A skipped rule the full warden finds violated after the turn turns the
// pre-guard off, counted, with why; a clean re-check only counts.
func TestAMissFoundAfterTheTurnTurnsThePreguardOff(t *testing.T) {
	preguardWorld(t)
	UpdatePreguard(func(r *PreguardRecord) { r.On = true })
	stub := &wardenStubLLM{reply: `{"rules":[{"rule":1,"why":"states a figure","answer":"violate"}]}`}
	app := &OrchestrateApp{}
	app.LLM = stub
	skipped := []guardrailRule{{Text: "never mention salary"}}
	preguardChecks.Store(preguardVerifyEvery - 1)
	app.verifySkipped(AgentRecord{Name: "X"}, guardHookPreOutput, "Jordan earns $85,000.", requesterIdentity{}, skipped)
	deadline := time.Now().Add(5 * time.Second)
	for LoadPreguard().On && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	r := LoadPreguard()
	if r.On || r.Missed != 1 || r.Verified != 1 || !strings.Contains(r.OffWhy, "never mention salary") {
		t.Errorf("after a miss: %+v", r)
	}
}

// The pre-guard's cases go through its question in full.
func TestThePreguardCasesRunThroughItsQuestion(t *testing.T) {
	caseAnswers(t, preguardRoute, []string{"engaged", "not_engaged"},
		caseReply{"other-subject", "not_engaged", []string{`{"why":"it is about the weather","answer":"not_engaged"}`}},
		caseReply{"refusal", "engaged", []string{`{"why":"it declines to share pay","answer":"engaged"}`}})
}
