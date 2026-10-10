package customapps

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A script's run_agent and run_pipeline reach the app's agent and pipeline as
// the person using the app, and draw on the same daily allowance as ask: a
// backend cannot spend around the cap by calling the other way.
func TestRunAgentAndPipelineShareAsksAllowance(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	prevAgent, prevPipe, prevAsk := appAgentRun, appPipelineRun, appAgentAsk
	t.Cleanup(func() { appAgentRun, appPipelineRun, appAgentAsk = prevAgent, prevPipe, prevAsk })
	var got []string
	appAgentRun = func(ctx context.Context, spec AppSpec, caller, agent, prompt string) (string, float64, error) {
		got = append(got, "agent "+caller+" "+agent+" "+prompt)
		return "the dice say 17", 0.30, nil
	}
	appPipelineRun = func(ctx context.Context, spec AppSpec, caller, pipeline, input string) (string, float64, error) {
		got = append(got, "pipeline "+caller+" "+input)
		return "verdict", 0.30, nil
	}
	appAgentAsk = func(ctx context.Context, owner, agentID, prompt string, jsonMode bool) (string, float64, error) {
		got = append(got, "ask")
		return "ok", 0.30, nil
	}
	spec := AppSpec{Slug: "game", Owner: "alice", AgentID: "dm", AskDailyUSD: 5, AskUserDailyUSD: 0.80}
	ctx := context.Background()

	if out, err := appscript.AppRunAgent(ctx, spec, "bob", "", "attack the drone"); err != nil || out != "the dice say 17" {
		t.Fatalf("run_agent: %q %v", out, err)
	}
	if out, err := appscript.AppRunPipeline(ctx, spec, "bob", "", "a topic"); err != nil || out != "verdict" {
		t.Fatalf("run_pipeline: %q %v", out, err)
	}
	// bob has spent 0.60 of his 0.80; one more call of any kind takes him over.
	appscript.AppAsk(ctx, spec, "bob", "hi", false)
	if _, err := appscript.AppRunAgent(ctx, spec, "bob", "", "again"); err == nil || !strings.Contains(err.Error(), "you have spent") {
		t.Fatalf("run_agent past bob's cap: %v", err)
	}
	if strings.Join(got, "|") != "agent bob  attack the drone|pipeline bob a topic|ask" {
		t.Errorf("calls = %q (a refused call must not reach the agent)", got)
	}
	if _, err := appscript.AppRunAgent(ctx, spec, "carol", "Quartermaster", strings.Repeat("x", runMaxPrompt+1)); err == nil {
		t.Error("an oversized prompt went through")
	}
}
