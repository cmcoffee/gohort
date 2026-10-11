package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A person waiting goes first whatever the agent is marked; the mark decides
// the agent's own runs; an unmarked agent keeps the lane it was reached on.
func TestAgentRunLane(t *testing.T) {
	bg := context.Background()
	chat := WithRunLane(bg, LaneInteractive)
	cases := []struct {
		name        string
		ctx         context.Context
		priority    string
		interactive bool
		want        RunLane
	}{
		{"schedule of an unmarked agent", bg, "", false, LaneBackground},
		{"schedule of a lowest agent", bg, "lowest", false, LaneLowest},
		{"schedule of an urgent agent", bg, "urgent", false, LaneInteractive},
		{"a person chatting with a lowest agent", bg, "lowest", true, LaneInteractive},
		{"a lowest agent dispatched from a chat", chat, "lowest", false, LaneInteractive},
		{"an unmarked agent dispatched from a chat", chat, "", false, LaneInteractive},
		{"a typo reads as unmarked", bg, "lowset", false, LaneBackground},
	}
	for _, c := range cases {
		got := RunLaneOf(agentRunLane(c.ctx, AgentRecord{RunPriority: c.priority}, c.interactive))
		if got != c.want {
			t.Errorf("%s: lane %s, want %s", c.name, got, c.want)
		}
	}
}
