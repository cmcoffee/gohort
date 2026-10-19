package orchestrate

// resolveWorkerTools is the order of its phases (workerCatalog). The two a
// catalog could lose without another test noticing: the reach an agent has
// through what it owns (its channels' chat tools, Builder for one allowed to
// hand it work), and what the session is told resolved.

import (
	"context"
	"slices"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestTheCatalogCarriesWhatTheAgentReachesAndTellsTheSession(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	a, err := saveAgent(udb, AgentRecord{Name: "Wiwee", Owner: user, OrchestratorPrompt: "p", AllowBuilderDispatch: true})
	if err != nil {
		t.Fatal(err)
	}
	prevThreads, had := ActiveChannelThreads()
	RegisterChannelThreads(strangerThreads{})
	t.Cleanup(func() {
		if had {
			RegisterChannelThreads(prevThreads)
		} else {
			RegisterChannelThreads(nil)
		}
	})
	SaveChannel(RootDB, Channel{ID: "c1", Owner: user, Service: "sms", AgentID: a.ID})
	turn := &chatTurn{app: app, agent: a, user: user, udb: udb, ctx: context.Background()}
	sess := turn.newToolSession()
	tools, names, err := turn.resolveWorkerTools(sess, true)
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, td := range tools {
		offered = append(offered, td.Tool.Name)
	}
	for _, want := range []string{"list_chats", "hand_to_builder"} {
		if !slices.Contains(offered, want) {
			t.Errorf("the catalog is missing %s: %v", want, offered)
		}
	}
	for _, n := range names {
		if !sess.HasTool(n) {
			t.Errorf("the session was not told %q resolved", n)
			break
		}
	}
	if len(names) == 0 {
		t.Error("nothing resolved")
	}
}
