// Which agent a command is being run FOR.
//
// The grant records are keyed by (agent, appliance), and the exec path had no
// idea which agent it was acting for — so a set of per-agent permissions sat in
// the database with nothing able to consult it. That is the missing half: the
// records describe who may do what, and this is how the runner finds out who it
// is.
//
// Carried on the context rather than threaded through the session signatures,
// the same way the parent run and the inherited workspace already travel. Both
// exec entry points take nine arguments already, and every path that could
// start one has a context in hand.
//
// EMPTY IS THE CONSOLE. A human driving servitor's own UI is not an agent, and
// resolving with an empty id falls through to the user's own auto-run
// settings — which is exactly what happened before any of this existed, so the
// console behaves identically.
package servitor

import (
	"context"
	"strings"
)

type actingAgentKey struct{}

// WithActingAgent stamps the agent a run acts for. Exported because the caller
// that knows is outside this package: an appliance tool handed to an agent
// stamps it from the tool session, which is the only place the two facts —
// which agent, which appliance — are both in hand.
func WithActingAgent(ctx context.Context, agentID string) context.Context {
	if strings.TrimSpace(agentID) == "" {
		return ctx
	}
	return context.WithValue(ctx, actingAgentKey{}, strings.TrimSpace(agentID))
}

// ActingAgent returns the agent this run acts for, or "" for a human at the
// console. Never inferred from anything the model or a remote caller supplies:
// an agent id that could be claimed would make the grants decorative.
func ActingAgent(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(actingAgentKey{}).(string)
	return id
}
