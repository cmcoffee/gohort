package orchestrate

// A run path that builds its own context has to carry BOTH network facts, and
// the way to be sure of that is for there to be one way to get them.
//
// The scheduled fire is what this is about. It mirrors the dispatch path by
// hand - its own ToolSession, its own loop - and it stamped neither the privacy
// connector nor the workspace ceiling. Both read as ALLOWED when absent, so
// the omission did not surface as a control that stopped working; it surfaced
// as an agent with MORE reach on a timer than the same agent has in chat.

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/netgate"
)

// The helper answers both halves, from the agent.
func TestBothNetworkFactsComeFromOneCall(t *testing.T) {
	pinRootDB(t)

	// An agent with nothing set: open, which is what every deployment that
	// has never touched either of these does today.
	ctx, conn := withAgentNetwork(context.Background(), "alice", AgentRecord{ID: "plain", Owner: "alice"}, false)
	if !conn.Allowed() {
		t.Error("an unrestricted agent lost its network")
	}
	if !netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("an unrestricted agent lost its workspace reach")
	}

	// Privacy closes the connector and leaves the workspace ceiling alone -
	// they are separate axes, and ANDed downstream.
	ctx, conn = withAgentNetwork(context.Background(), "alice",
		AgentRecord{ID: "private", Owner: "alice", ForcePrivate: true}, true)
	if conn.Allowed() {
		t.Error("a private turn got an open connector")
	}
	if !netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("privacy moved the workspace ceiling, which is a different question")
	}

	// The ceiling closes the workspace and leaves the agent's network TOOLS
	// alone, which is the whole reason it is not just ForcePrivate.
	ctx, conn = withAgentNetwork(context.Background(), "alice",
		AgentRecord{ID: "walled", Owner: "alice", WorkspaceNoNetwork: true}, false)
	if !conn.Allowed() {
		t.Error("the workspace ceiling took the agent's tools with it")
	}
	if netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("the agent's ceiling was ignored")
	}
}

// Every path that starts a turn takes both, and the unattended one is the
// reason this is a test rather than a convention.
func TestEveryRunPathStampsTheTurnsNetwork(t *testing.T) {
	fire := mustReadFile(t, "scheduled_updates.go")
	if !strings.Contains(fire, "withAgentNetwork(ctx, p.Username, agent,") {
		t.Error("a scheduled fire runs with no network state, so an agent has more reach on a timer than in chat")
	}
	// On the SESSION as well as the context. NetworkAllowed reads the field
	// and the ceiling reads the context, and WorkspaceNetworkAllowed ANDs
	// them, so one of the two set is half a gate.
	if !strings.Contains(fire, "Network: fireConnector,") {
		t.Error("the fire's session has no connector, so NetworkAllowed answers off a nil field")
	}

	live := mustReadFile(t, "runner_http.go")
	if !strings.Contains(live, "withAgentNetwork(ctx, user, agent, privateMode)") {
		t.Error("the live turn stopped going through the one place both facts are set")
	}

	// And one place builds them. A second NewNetworkConnector on a run path is
	// how the two halves come apart again - the dispatch path's own is not one
	// (it narrows an EXISTING context rather than starting one).
	for _, name := range []string{"scheduled_updates.go", "runner_http.go"} {
		if strings.Contains(mustReadFile(t, name), "NewNetworkConnector(") {
			t.Errorf("%s builds its own connector again instead of taking both facts together", name)
		}
	}
}
