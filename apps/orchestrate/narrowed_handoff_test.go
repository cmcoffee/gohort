package orchestrate

// A narrowed agent's first handoff to an agent it is not yet allowed to reach
// is a card when someone is watching. From a channel there was no one, so it
// was refused on the spot, nothing reached the owner, and the agent told them
// it was waiting in a pane where it was not. It now waits on the Requests tab,
// and approving it "always" saves the pairing so the next one runs.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func narrowedFixture(t *testing.T, turns ...FakeTurn) (*dispatchFixture, AgentRecord) {
	t.Helper()
	df := newDispatchFixture(t, turns...)
	df.agent.AllowedTools = []string{"workspace"} // narrowed: a real list, and "agents" rides every top-level agent
	if _, err := saveAgent(df.udb, df.agent); err != nil {
		t.Fatal(err)
	}
	scout, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You make songs."})
	if err != nil {
		t.Fatal(err)
	}
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	return df, scout
}

func queuedFor(agent string) []Authorization {
	var out []Authorization
	for _, a := range ListAuthorizations(RootDB, "u") {
		if a.Action == "" && a.Agent == agent {
			out = append(out, a)
		}
	}
	return out
}

func TestANarrowedAgentsFirstHandoffFromAChannelWaitsForTheOwner(t *testing.T) {
	// Its own thread: a finished task's wake coalesces per session id across
	// the process, so a shared id hands this wake to another test's window.
	room := "room-" + UUIDv4()
	df, scout := narrowedFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "Scout", "message": "make the song"}}}},
		FakeTurn{Content: "It is waiting on your approval.", Repeat: true})
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: room, Message: "have Scout make the song", MessageSender: "u", SenderIsOwner: true})

	q := queuedFor("Scout")
	if len(q) != 1 || q[0].FromSession != room || q[0].Brief != "make the song" {
		t.Fatalf("the handoff should wait on the Requests tab with where it came from: %+v", q)
	}
	told := false
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			for _, r := range m.ToolResults {
				if r.ID == "c1" && strings.Contains(r.Content, "queued for them on the Requests tab") {
					told = true
				}
			}
		}
	}
	if !told {
		t.Error("the agent was not told, truthfully, where the request is")
	}

	// Always allow saves the pairing: the next handoff needs no one.
	r := httptest.NewRequest(http.MethodPost, "/api/console/approvals/always?id="+q[0].ID, nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")})
	w := httptest.NewRecorder()
	before := len(df.thread(room).Messages)
	routed(df.app)(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("approve answered %d %s", w.Code, w.Body.String())
	}
	// Approving runs the handoff in the background and wakes the chat with
	// its result. Wait for that, or it lands in whichever test runs next.
	deadline := time.Now().Add(10 * time.Second)
	for len(df.thread(room).Messages) <= before && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(df.thread(room).Messages) <= before {
		t.Error("the approved handoff's result never came back to the chat that asked")
	}
	turn := &chatTurn{app: df.app, user: "u", udb: df.udb, agent: df.agent}
	if !turn.dispatchEdgeApproved(scout) {
		t.Error("Always allow on the queued handoff did not save the pairing")
	}
}

// Handed off (await false) from a turn nobody watches, the same two waits:
// a narrowed agent's first handoff, and Builder.
func TestAHandoffWithNobodyWatchingWaitsBeforeItDetaches(t *testing.T) {
	df, _ := narrowedFixture(t, FakeTurn{Content: "ok", Repeat: true})
	turn := &chatTurn{app: df.app, user: "u", udb: df.udb, agent: df.agent, ctx: t.Context()}
	sess := &ToolSession{Username: "u", ChatSessionID: "room-1", DB: df.udb, Ctx: t.Context()}
	pre := turn.agentsDispatchPolicy(true).Preflight
	err := pre(map[string]any{"action": "run", "agent": "Scout", "message": "make the song", "await": false}, sess)
	if err == nil || !strings.Contains(err.Error(), "Requests tab") || len(queuedFor("Scout")) != 1 {
		t.Errorf("a narrowed handoff should wait for the owner before detaching: %v, %d queued", err, len(queuedFor("Scout")))
	}

	df.agent.AllowedTools = nil
	df.agent.AllowBuilderDispatch = true
	turn.agent = df.agent
	err = pre(map[string]any{"action": "run", "agent": "seed-builder", "message": "make a map tool", "await": false}, sess)
	if err == nil || !strings.Contains(err.Error(), "Builder changes things") || len(queuedFor("Builder")) != 1 {
		t.Errorf("Builder handed off unwatched should wait for the owner: %v, %d queued", err, len(queuedFor("Builder")))
	}
}
