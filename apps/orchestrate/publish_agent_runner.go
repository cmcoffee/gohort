package orchestrate

// The agent half of an agent-backed publish destination.
//
// core/docs owns the destination registry and must not know how to run an
// agent; this app owns the agent loop and must not know what publishing is.
// The seam between them is a registered closure, the same shape as the channel
// runner and the standing runner above it.

import (
	"context"
	"errors"
	"strings"

	"github.com/cmcoffee/gohort/core/docs"
)

// registerAgentPublisher installs the closure core/docs calls when a publish is
// routed to an agent. Call once at startup.
func registerAgentPublisher(app *OrchestrateApp) {
	docs.RegisterAgentPublisher(func(ctx context.Context, user, agent, instruction string) (string, error) {
		if app == nil {
			return "", errors.New("orchestrate runtime not initialized")
		}
		// agentOwner == runtimeUser: publishing runs as the person who asked
		// for it, under their own store, so the destination agent's tools and
		// credentials are the ones that person is entitled to. A destination
		// that published as somebody else would be a way to borrow their
		// access by choosing it from a menu.
		//
		// Confirmations are DECLINED rather than left unanswered. This runs
		// with nobody watching it — the publish came from a button, and the
		// dialog that could answer a question has already moved on — and a nil
		// confirm would fail closed anyway. Declining says so in the reply,
		// which is what the destination records.
		run, err := app.runAgentSyncConfirm(ctx, user, user, agent, instruction,
			func(string, string) bool { return false }, "publish")
		if err != nil {
			return "", err
		}
		said := strings.TrimSpace(run.Text)
		if run.HitRoundCap {
			// Said out loud rather than returned as a clean result: a run that
			// ran out of rounds may have done half the job, and a publish
			// record that reads as a success would be the wrong account of it.
			said = strings.TrimSpace(said + "\n\n(This run stopped at its round limit, so it may not have finished.)")
		}
		if said == "" {
			return "", errors.New("the agent finished without saying what it did")
		}
		return said, nil
	})
}
