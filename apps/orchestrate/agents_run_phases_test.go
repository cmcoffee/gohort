package orchestrate

// agentsRunActionFor is the order of its phases (agentsRun). The three a
// dispatch could lose without any other test noticing: the line that tells
// the person the agent was sent, the stop on an attachment the caller does not
// hold, and the stop when the agent's own run fails.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func inlineRun(t *testing.T, turns ...FakeTurn) (*dispatchFixture, *chatTurn, *bytes.Buffer) {
	t.Helper()
	df := newDispatchFixture(t, turns...)
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You find things."}); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	return df, &chatTurn{app: df.app, user: "u", udb: df.udb, agent: df.agent, ctx: t.Context(), sse: &sseWriter{live: buf}}, buf
}

func TestARunSaysTheAgentWasSent(t *testing.T) {
	_, turn, buf := inlineRun(t, FakeTurn{Content: "Found it.", Repeat: true})
	if _, err := turn.agentsRunActionFor(toolCard{}, nil, map[string]any{"agent": "Scout", "message": "find it"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[Scout] dispatched") {
		t.Errorf("the page was not told the agent was sent:\n%.500s", buf.String())
	}
}

func TestARunStopsOnAnAttachmentTheCallerDoesNotHold(t *testing.T) {
	df, turn, _ := inlineRun(t, FakeTurn{Content: "Found it.", Repeat: true})
	_, err := turn.agentsRunActionFor(toolCard{}, &ToolSession{Username: "u"}, map[string]any{"agent": "Scout", "message": "use this", "attach": []any{"media#4"}})
	if err == nil || !strings.Contains(err.Error(), "media#4") {
		t.Fatalf("an attachment the caller does not hold should stop the run: %v", err)
	}
	if n := df.llm.Calls(); n != 0 {
		t.Errorf("Scout ran %d model call(s) without what it was meant to be handed", n)
	}
}

func TestARunReportsItsAgentsFailure(t *testing.T) {
	_, turn, _ := inlineRun(t, FakeTurn{Err: errors.New("the model is down"), Repeat: true})
	out, err := turn.agentsRunActionFor(toolCard{}, nil, map[string]any{"agent": "Scout", "message": "find it"})
	if err == nil || !strings.Contains(err.Error(), "the model is down") {
		t.Fatalf("the agent's failure should come back as the call's error: %q %v", out, err)
	}
}

// The agents tool's run is held to the agent's own caps, as every other run
// path is. It set none of them: an agent run this way spent past its daily cap
// and its action quotas, and its duplicate-send guard was off.
func TestARunCarriesTheAgentsCaps(t *testing.T) {
	_, turn, _ := inlineRun(t)
	target := AgentRecord{ID: "scout-id", Name: "Scout", Owner: "u",
		DailySpendUSD: 0.5, ActionQuotas: ActionQuotaMap{"send_message": 3}}
	// The sub-turn runs the target, as subTurn is built in production.
	sub := &chatTurn{app: turn.app, user: turn.user, udb: turn.udb, agent: target, ctx: t.Context(), sse: turn.sse}
	ar := &agentsRun{t: turn, target: target, subTurn: sub, subSess: &ToolSession{Username: "u"}, ctx: t.Context()}
	ar.configureLoop()
	cfg := ar.runCfg
	if cfg.BudgetKey != "scout-id" || cfg.DailySpendUSD != 0.5 {
		t.Errorf("the daily spend cap is not applied: key %q, cap %v", cfg.BudgetKey, cfg.DailySpendUSD)
	}
	if cfg.ActionQuotas["send_message"] != 3 {
		t.Errorf("the action quotas are not applied: %v", cfg.ActionQuotas)
	}
	if cfg.SendGuardKey == nil {
		t.Error("the duplicate-send guard is off")
	}
}
