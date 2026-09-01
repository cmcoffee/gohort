package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Two agents can have a real claim on one standing agent, and the surfaces used
// to pick one: scope by the reporter and the agent actually running the mission
// every morning shows nothing about it; scope by the runner and it vanishes from
// the controller that created it. Both were shipped, one after the other, each
// fixing the other's blind spot.
func TestStandingAgentShowsOnRunnerAndController(t *testing.T) {
	sa := StandingAgent{Name: "market-research", AgentID: "researcher", ReportAgentID: "operator"}

	if !standingAgentOnRailOf(sa, "researcher") {
		t.Error("the agent that RUNS the mission must show it — this is the case a user notices, because they can watch it happen")
	}
	if !standingAgentOnRailOf(sa, "operator") {
		t.Error("the controller that created it must keep showing it")
	}
	if standingAgentOnRailOf(sa, "unrelated") {
		t.Error("an agent with no claim must not carry someone else's schedule")
	}
	if !standingAgentOnRailOf(sa, "") {
		t.Error("an unscoped view lists everything")
	}
}

// A pipeline schedule has no runner agent at all, so the creating agent is the
// only link it will ever have to one. Worth pinning: it explains why such a
// schedule is on exactly one rail no matter how the scoping is widened.
func TestPipelineScheduleLivesOnItsCreatorsRailOnly(t *testing.T) {
	sa := StandingAgent{Name: "daily-stories-9am", PipelineID: "news", ReportAgentID: "operator"}
	if !standingAgentOnRailOf(sa, "operator") {
		t.Error("it belongs on the rail of the agent whose session set it up")
	}
	if standingAgentOnRailOf(sa, "some-other-agent") {
		t.Error("nothing about a pipeline schedule makes it another agent's")
	}

	// The one that would otherwise fall out of every surface.
	orphan := StandingAgent{Name: "orphan", PipelineID: "news"}
	if !standingAgentOnRailOf(orphan, "anyone") {
		t.Error("a schedule that names neither a runner nor a controller must still appear somewhere")
	}
}

// A row showing up on two agents has to read correctly on both, or it looks
// duplicated by accident.
func TestStandingRoleSuffixSaysWhyItIsHere(t *testing.T) {
	sa := StandingAgent{AgentID: "researcher", ReportAgentID: "operator"}

	if got := standingRoleSuffix(nil, sa, "researcher"); !strings.Contains(got, "set up from") {
		t.Errorf("on the runner's rail it should say where it came from; got %q", got)
	}
	if got := standingRoleSuffix(nil, sa, "operator"); !strings.Contains(got, "runs as") {
		t.Errorf("on the controller's rail it should say who runs it; got %q", got)
	}
	// Nothing to explain when one agent is both, or when there is no second one.
	same := StandingAgent{AgentID: "solo", ReportAgentID: "solo"}
	if got := standingRoleSuffix(nil, same, "solo"); got != "" {
		t.Errorf("one agent in both roles needs no explanation; got %q", got)
	}
	if got := standingRoleSuffix(nil, StandingAgent{PipelineID: "p", ReportAgentID: "operator"}, "operator"); got != "" {
		t.Errorf("a pipeline schedule has no runner to name; got %q", got)
	}
}
