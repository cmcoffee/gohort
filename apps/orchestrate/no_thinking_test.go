package orchestrate

// A caller waiting on the line hears nothing while a model thinks, so a run
// under WithoutThinking does not think, whatever the agent's own Reasoning
// says, and neither does an agent it hands the work to.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// thinkOf is whether the call that carried want as its live message asked to
// think, and that call's effort: the judges call the same model, so position
// says nothing.
func (df *dispatchFixture) thinkOf(want string) (think bool, effort string) {
	df.t.Helper()
	for i := 0; i < df.llm.Calls(); i++ {
		msgs := df.llm.Sent(i)
		if n := len(msgs); n > 0 && msgs[n-1].Role == "user" && strings.Contains(msgs[n-1].Content, want) {
			cfg := df.llm.Config(i)
			return cfg.Think != nil && *cfg.Think, cfg.Effort
		}
	}
	df.t.Fatalf("no call carried %q", want)
	return false, ""
}

func TestACallerOnTheLineStopsThinkingDownTheChain(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Order 42 ships Tuesday", Repeat: true})
	df.agent.Effort = "high"
	a, err := saveAgent(df.udb, df.agent)
	if err != nil {
		t.Fatal(err)
	}
	df.agent = a
	on := true

	// The agent thinks when nobody is waiting.
	df.run(AgentSyncRun{SubSessionID: "s1", Message: "first question", Think: &on})
	if think, _ := df.thinkOf("first question"); !think {
		t.Fatal("an agent set to think did not, with nobody waiting")
	}

	// Under the mark it does not, even asked to.
	quiet := WithoutThinking(t.Context())
	if _, err := df.app.RunAgentSyncContinuingRich(quiet, AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID, SubSessionID: "s2", Message: "second question", Think: &on}); err != nil {
		t.Fatal(err)
	}
	if think, _ := df.thinkOf("second question"); think {
		t.Error("a run with a caller on the line thought")
	}

	// A hand-off under the mark does not either.
	if _, err := df.app.RunAgentSync(quiet, "u", "u", df.agent.ID, "third question"); err != nil {
		t.Fatal(err)
	}
	if think, _ := df.thinkOf("third question"); think {
		t.Error("a handed-off run thought with a caller on the line")
	}
	if _, err := df.app.RunAgentSync(t.Context(), "u", "u", df.agent.ID, "fourth question"); err != nil {
		t.Fatal(err)
	}
	if think, _ := df.thinkOf("fourth question"); !think {
		t.Error("a handed-off run with nobody waiting should keep the agent's thinking")
	}

	// A caller that asked for a level gets it in place of the agent's.
	df.run(AgentSyncRun{SubSessionID: "s3", Message: "fifth question", Think: &on, Effort: "low"})
	if think, effort := df.thinkOf("fifth question"); !think || effort != "low" {
		t.Errorf("the caller's level should stand in for the agent's, got think=%v effort=%q", think, effort)
	}
}

// A hand-off that builds its context from a session's, not the call's, keeps
// the mark with the lineage.
func TestTheNoThinkingMarkCrossesAHandOff(t *testing.T) {
	from := WithoutThinking(context.Background())
	if chainThink(carryDispatchLineage(from, context.Background()), true) {
		t.Error("the mark was lost on a hand-off")
	}
	if !chainThink(carryDispatchLineage(context.Background(), context.Background()), true) {
		t.Error("a hand-off with no mark should keep thinking")
	}
}
