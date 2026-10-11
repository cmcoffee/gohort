package orchestrate

import (
	"context"

	. "github.com/cmcoffee/gohort/core"
)

// agentRunLane is the lane a run of this agent goes out on (core.RunLane).
// A person waiting goes first whatever the agent is marked: an "urgent"
// agent is a person's lane on every run, a "lowest" one gives way to
// anything on its own runs but still answers at once when someone is
// chatting with it or with the agent that dispatched it. Unmarked, the run
// keeps the lane it inherited, which is background for a schedule or a
// monitor and the chat's for a dispatch from a chat.
func agentRunLane(ctx context.Context, a AgentRecord, interactive bool) context.Context {
	switch {
	case interactive || a.RunPriority == "urgent":
		return WithRunLane(ctx, LaneInteractive)
	case a.RunPriority == "lowest" && RunLaneOf(ctx) != LaneInteractive:
		return WithRunLane(ctx, LaneLowest)
	}
	return ctx
}
