package docs

// What a publish is doing, step by step, for whoever is watching it.
//
// A publish through an integration is a model run with tools: it looks the
// page up, converts, updates. From outside it was a spinner and then an
// outcome, so a publish that "worked" without fixing what it was meant to fix
// left nothing to look at. The steps ride on the context, so a destination
// reports them with one call and no signature changes, and a producer that
// wants them (Scribe's publish job) puts a reporter on the context it passes.

import (
	"context"
	"fmt"
)

type publishStepsKey struct{}

// WithPublishSteps returns ctx with report receiving each step a publish on it
// takes. report must be safe to call from the publish's goroutines.
func WithPublishSteps(ctx context.Context, report func(step string)) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, publishStepsKey{}, report)
}

// PublishStep reports one step of the publish running on ctx, as a line a
// person reads. A no-op when nobody is watching.
func PublishStep(ctx context.Context, format string, a ...any) {
	if ctx == nil {
		return
	}
	if report, ok := ctx.Value(publishStepsKey{}).(func(string)); ok {
		report(fmt.Sprintf(format, a...))
	}
}

type publishActivityKey struct{}

// WithPublishActivity returns ctx with report receiving what a publish on it is
// doing right now ("round 2 · Thinking · 14s"): a line that replaces the last
// one, where a step adds to a list. For a watcher with no live surface of its
// own to draw a run's rounds on (Scribe's publish job). report must be safe to
// call from the publish's goroutines.
func WithPublishActivity(ctx context.Context, report func(now string)) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, publishActivityKey{}, report)
}

// PublishActivity reports what the publish running on ctx is doing now. A
// no-op when nobody is watching.
func PublishActivity(ctx context.Context, format string, a ...any) {
	if ctx == nil {
		return
	}
	if report, ok := ctx.Value(publishActivityKey{}).(func(string)); ok {
		report(fmt.Sprintf(format, a...))
	}
}
