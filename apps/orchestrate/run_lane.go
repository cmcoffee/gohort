package orchestrate

import (
	"context"
	"fmt"
	"time"

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

// waitNotice is an OnWait for a run this turn shows: the run giving way to
// people is one status line in the activity pane, updated in place while
// the wait lasts and settled with the whole wait when the call goes, so a
// held run looks held rather than slow. name prefixes the line for a run
// of another agent this turn started; empty for the turn's own.
func (t *chatTurn) waitNotice(name string) func(waited time.Duration, done bool) {
	var id string
	return func(waited time.Duration, done bool) {
		if t == nil || t.sse == nil {
			return
		}
		secs := int(waited.Round(time.Second) / time.Second)
		text := fmt.Sprintf("Waiting for the model: a person is using it (%ds)", secs)
		if done {
			text = fmt.Sprintf("Waited %ds for the model: a person was using it", secs)
		}
		if name != "" {
			text = "[" + name + "] " + text
		}
		if id == "" {
			id = activityCheapID()
			t.sse.Send(map[string]any{"kind": "activity", "type": "status", "id": id, "text": text})
		} else {
			t.sse.Send(map[string]any{"kind": "activity_update", "id": id, "text": text})
		}
		if done {
			id = ""
		}
	}
}
