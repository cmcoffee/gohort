package orchestrate

// A caller waiting on the line.
//
// A voice assistant or a phone call hears nothing while a model thinks, and
// hangs up on a silent line. The API turn ran with thinking off, but an agent
// it handed the work to went by its own Reasoning setting, so the silence came
// back one hop down. The mark rides the context the way the dispatch lineage
// does, and every agent run under it, and every hand-off from there, skips
// thinking. The quick checks around a run (the warden, the judges) keep their
// own settings: they are guards, not the work.

import (
	"context"

	"github.com/cmcoffee/gohort/core/agenthost"
)

// chainThink is think, unless ctx carries agenthost.WithoutThinking. The mark
// lives in agenthost because the caller that sets it (the API) is another app.
func chainThink(ctx context.Context, think bool) bool {
	if agenthost.ThinkingOff(ctx) {
		return false
	}
	return think
}
