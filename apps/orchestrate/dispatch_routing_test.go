package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A run another agent asked for, a bridge message, a monitor wake or a
// scheduled fire follows the target's own "Use Lead model", the same as a
// direct chat, and privacy still wins.
func TestDispatchedRunsFollowTheTargetsLeadSetting(t *testing.T) {
	open := WithNetworkConnector(context.Background(), NewNetworkConnector(false))
	closed := WithNetworkConnector(context.Background(), NewNetworkConnector(true))

	lead := AgentRecord{ID: "a-lead", LeadModel: true}
	cases := []struct {
		name  string
		ctx   context.Context
		agent AgentRecord
		pin   LLMTier
		route string
	}{
		{"lead agent, open turn", open, lead, LEAD, orchestratorRouteKey("a-lead", true)},
		{"lead agent, no connector", context.Background(), lead, LEAD, orchestratorRouteKey("a-lead", true)},
		{"lead agent, private turn", closed, lead, TierUnset, "app.orchestrate.worker"},
		{"lead agent that forces private", open, AgentRecord{ID: "a-fp", LeadModel: true, ForcePrivate: true}, TierUnset, "app.orchestrate.worker"},
		{"worker agent", open, AgentRecord{ID: "a-w"}, TierUnset, "app.orchestrate.worker"},
	}
	for _, c := range cases {
		pin, route := dispatchRouting(c.ctx, &chatTurn{agent: c.agent})
		if pin != c.pin || route != c.route {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", c.name, pin, route, c.pin, c.route)
		}
	}
	if pin, route := dispatchRouting(open, nil); pin != TierUnset || route != "app.orchestrate.worker" {
		t.Errorf("nil turn: got (%v, %q)", pin, route)
	}
}
