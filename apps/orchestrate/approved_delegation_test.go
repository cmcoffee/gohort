package orchestrate

// An approved delegation behaves like a pre-authorized one: it runs under the
// asking conversation's privacy and its result wakes that conversation. It used
// to run on a bare context and report into the target's own thread, so the
// asking agent kept telling the user it was still waiting, and a Private
// conversation's approved delegations searched the web.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// stubDelegationRunner installs the real reporter with a runner that records
// whether the run could reach the network and returns a fixed result.
func stubDelegationRunner(t *testing.T, app *OrchestrateApp) *[]bool {
	t.Helper()
	registerStandingRunner(app)
	var network []bool
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		network = append(network, NetworkAllowedFromContext(ctx))
		return StandingRunResult{Status: RunOK, Summary: "short summary",
			Raw: "FULL RESULT: three stories about the release, with links"}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	return &network
}

func TestAQueuedDelegationRecordsWhereItWasAskedFrom(t *testing.T) {
	depStores(t, "u")
	sess := &ToolSession{Username: "u", ChatSessionID: "s-ask", ChannelChatID: "chat-1", Network: NewNetworkConnector(true)}
	var delegate AgentToolDef
	for _, td := range operatorManagementTools(sess, "lead") {
		if td.Tool.Name == "delegate" {
			delegate = td
		}
	}
	out, err := delegate.Handler(context.Background(), map[string]any{"agent": "helper", "brief": "find the news"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "comes back to you here") || !strings.Contains(out, "Private") {
		t.Errorf("the asking agent should hear the result comes back, and that the run will be Private:\n%s", out)
	}
	auths := ListAuthorizations(RootDB, "u")
	if len(auths) != 1 {
		t.Fatalf("authorizations = %+v", auths)
	}
	a := auths[0]
	if a.FromSession != "s-ask" || a.FromChatID != "chat-1" || a.FromAgent != "lead" || !a.FromPrivate {
		t.Errorf("the queued request should carry its origin and privacy: %+v", a)
	}
}

func TestAnApprovedDelegationWakesTheAskerAndStaysPrivate(t *testing.T) {
	root := depStores(t, "u")
	app := orchRef
	network := stubDelegationRunner(t, app)
	sid := "s-deleg-" + UUIDv4()
	q := registerInjectionQueue(sid, "u", "lead")
	t.Cleanup(func() { releaseInjectionQueue(sid) })

	app.runApprovedDelegation(Authorization{Owner: "u", Agent: "helper", Brief: "find the news",
		FromAgent: "lead", FromSession: sid, FromPrivate: true}, "helper")

	if len(*network) != 1 || (*network)[0] {
		t.Fatalf("an approved delegation from a Private conversation must run Private, network seen = %v", *network)
	}
	notes := q.Drain()
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "FULL RESULT") || !strings.Contains(notes[0].Text, "approved") {
		t.Fatalf("the asking conversation should be woken with the full result: %+v", notes)
	}
	if !strings.Contains(notes[0].Text, "ran Private") {
		t.Errorf("the note should say the run was Private: %s", notes[0].Text)
	}
	if s, ok := loadChatSession(UserDB(root, "u"), "helper", cortexSessionID("helper")); ok && len(s.Messages) > 0 {
		t.Errorf("the result was also posted into the target's own thread: %+v", s.Messages)
	}
}

// A request that recorded no origin (a legacy record, or one queued with
// nobody watching) keeps the old behaviour: it runs, and reports into the
// target's thread.
func TestAnApprovedDelegationWithNoOriginReportsTheOldWay(t *testing.T) {
	root := depStores(t, "u")
	app := orchRef
	network := stubDelegationRunner(t, app)
	app.runApprovedDelegation(Authorization{Owner: "u", Agent: "helper", Brief: "find the news"}, "helper")
	if len(*network) != 1 || !(*network)[0] {
		t.Fatalf("a request that was not Private runs with the network, seen = %v", *network)
	}
	s, ok := loadChatSession(UserDB(root, "u"), "helper", cortexSessionID("helper"))
	if !ok || len(s.Messages) == 0 || !strings.Contains(s.Messages[len(s.Messages)-1].Content, "FULL RESULT") {
		t.Errorf("with no origin the report lands in the target's thread as before: %+v", s)
	}
}
