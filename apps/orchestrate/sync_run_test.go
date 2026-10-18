package orchestrate

// A turn-free dispatch end to end, against the scripted model: the run a
// standing fire, a delegation and a pipeline's agent stage go through. Its
// callers record what it reports to the ledger, so the report is what these
// read: the reply, the trace, the round cap, the spend, the run's own row.

import (
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func allowAll(string, string) bool { return true }

// sync runs the fixture's agent the way a standing fire does, with the
// caller's lookup tool, and no runtime user: it defaults to the owner.
func (df *dispatchFixture) sync(agentKey, message string) (syncRunResult, error) {
	return df.app.runAgentSyncAppTools(df.t.Context(), "u", "", agentKey, message, allowAll, []AgentToolDef{df.lookupTool()}, nil)
}

// ownRow is the run's own row on the live surface, which this path registers
// when its caller did not.
func (df *dispatchFixture) ownRow(name string) (RunSnapshot, bool) {
	for _, r := range df.app.runsRegistry().Activity("u") {
		if r.Kind == "dispatch" && r.AgentName == name {
			return r, true
		}
	}
	return RunSnapshot{}, false
}

func TestASyncRunReportsWhatItDid(t *testing.T) {
	const reply = "Order A17 shipped Tuesday."
	tool := lookupCall("A17")
	tool.InputTokens, tool.OutputTokens = 120, 30
	df := newDispatchFixture(t, tool, FakeTurn{Content: reply, Repeat: true, InputTokens: 140, OutputTokens: 20})
	res, err := df.sync("Clerk", "Where is order A17?")
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if res.Text != reply || res.HitRoundCap {
		t.Errorf("text=%q hit_cap=%v, want the reply and no cap", res.Text, res.HitRoundCap)
	}
	if len(df.looked) != 1 || df.looked[0] != "A17" {
		t.Errorf("the caller's tool was not run with the model's arguments: %v", df.looked)
	}
	if len(res.Trace) == 0 || res.Trace[0].Name != "lookup_order" {
		t.Errorf("the report carries no trace of what the run did: %+v", res.Trace)
	}
	if res.Usage.WorkerInput+res.Usage.WorkerOutput+res.Usage.WorkerCacheRead == 0 {
		t.Errorf("the report says the run spent nothing: %+v", res.Usage)
	}
	if row, ok := df.ownRow("Clerk"); !ok || row.Status != RunStatusCompleted {
		t.Errorf("the run's own live row should end completed: %+v %v", row, ok)
	}
}

func TestASyncRunReportsAFailedModel(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Err: errors.New("model unavailable"), Repeat: true})
	if _, err := df.sync("Clerk", "Where is order A17?"); err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Errorf("err = %v, want the model's", err)
	}
	if row, ok := df.ownRow("Clerk"); !ok || row.Status != RunStatusFailed {
		t.Errorf("the run's own live row should end failed: %+v %v", row, ok)
	}
}

// A run that spends its rounds says so, so its caller can flag itself
// incomplete rather than report a truncated run as ok.
func TestASyncRunThatRunsOutOfRoundsSaysSo(t *testing.T) {
	looping := lookupCall("A17")
	looping.Repeat = true
	df := newDispatchFixture(t, looping)
	res, err := df.sync("Clerk", "Check order A17 until it ships.")
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if !res.HitRoundCap {
		t.Errorf("a run that never stopped calling tools did not report the cap: %+v", res)
	}
}

// A run that ends on a promise it did not keep leaves it on the books for the
// next turn on its thread; a run that does work clears it.
func TestASyncRunKeepsTheBooksOnWhatItPromised(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "On it, let me grab the shipping records for that.", Repeat: true})
	if _, err := df.sync("Clerk", "Where is order A17?"); err != nil {
		t.Fatal(err)
	}
	thread := "external-dispatch:u:" + df.agent.ID
	if _, ok := openCommitmentFor(df.udb, thread); !ok {
		t.Fatal("an unkept promise was not recorded")
	}
	// A fresh script: the fake picks its turn by how many calls it has had.
	df.llm = &FakeLLM{Turns: []FakeTurn{lookupCall("A17"), {Content: "It shipped Tuesday.", Repeat: true}}}
	df.app.LLM = df.llm
	if _, err := df.sync("Clerk", "Well?"); err != nil {
		t.Fatal(err)
	}
	if _, ok := openCommitmentFor(df.udb, thread); ok {
		t.Error("a run that did the work left the promise open")
	}
}

// Builder handed a brief runs it headless: the brief arrives marked as a
// delegated one-shot.
func TestASyncRunHandsBuilderItsBriefAsDelegated(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Built it.", Repeat: true})
	if _, err := saveAgent(df.udb, AgentRecord{ID: "seed-builder", Name: "Builder", Owner: "u", OrchestratorPrompt: "You build."}); err != nil {
		t.Fatal(err)
	}
	if _, err := df.sync("seed-builder", "Make a tool that lists orders."); err != nil {
		t.Fatal(err)
	}
	if df.request("[DELEGATED INVOCATION]") == nil || df.request("Brief: Make a tool that lists orders.") == nil {
		t.Error("Builder was not handed its brief as a delegated one-shot")
	}
}
