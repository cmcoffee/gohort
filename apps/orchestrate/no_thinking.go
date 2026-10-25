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

import "context"

type noThinkingKey struct{}

// WithoutThinking marks ctx so that no agent run under it thinks: the run it
// starts, and every hand-off from there.
func WithoutThinking(ctx context.Context) context.Context {
	return context.WithValue(ctx, noThinkingKey{}, true)
}

// chainThink is think, unless ctx carries WithoutThinking.
func chainThink(ctx context.Context, think bool) bool {
	if ctx == nil {
		return think
	}
	if off, _ := ctx.Value(noThinkingKey{}).(bool); off {
		return false
	}
	return think
}
