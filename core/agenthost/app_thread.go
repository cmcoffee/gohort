package agenthost

// A conversation an app's script continues with its agent.

import (
	"context"
	"strings"
)

// AppThread names the conversation an app's script continues with its agent
// (gohort.run_agent(prompt, thread=...)): its name as the script gives it,
// whether to start it over, and the key of the outside client the call came
// from ("" for the app's own page). It rides the run's context from the
// script's hook to the agent host, which keeps each thread apart per person
// and key, so a script can never reach another caller's conversation.
type AppThread struct {
	Name  string
	Key   string
	Fresh bool
}

type appThreadKey struct{}

// WithAppThread is ctx carrying t, when it names a thread.
func WithAppThread(ctx context.Context, t AppThread) context.Context {
	if strings.TrimSpace(t.Name) == "" {
		return ctx
	}
	return context.WithValue(ctx, appThreadKey{}, t)
}

// AppThreadFrom is the thread ctx's run continues, zero for none.
func AppThreadFrom(ctx context.Context) AppThread {
	if ctx == nil {
		return AppThread{}
	}
	t, _ := ctx.Value(appThreadKey{}).(AppThread)
	return t
}
