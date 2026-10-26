package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An agent's own ask mark binds that agent, the fleet-wide one binds every
// agent, and a run one agent hands to another carries the caller's marks.
func TestAsksFirstPerAgentEverywhereAndThroughDelegation(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	db := AuthDB()
	research := &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a-research", Owner: "alice"}}
	other := &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a-other", Owner: "alice"}}

	SetUserToolAsksInChat(db, "alice", "a-research", "web_search", true)
	if !research.asksFirst("web_search") || other.asksFirst("web_search") {
		t.Error("a per-agent mark did not bind exactly that agent")
	}

	// Handed over: the delegate asks because its caller does.
	delegate := &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a-helper", Owner: "alice"}, askChain: research.delegatedAskChain()}
	if !delegate.asksFirst("web_search") {
		t.Error("a delegated run went around the caller's ask")
	}
	onward := &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a-third", Owner: "alice"}, askChain: delegate.delegatedAskChain()}
	if !onward.asksFirst("web_search") {
		t.Error("the caller's ask was lost one delegation further down")
	}

	SetUserToolAsksInChat(db, "alice", "", "fetch_url", true)
	if !other.asksFirst("fetch_url") || !research.asksFirst("fetch_url") {
		t.Error("an everywhere mark did not bind every agent")
	}
}

// The ask reaches the person even for a tool that declares no confirmation,
// with the call itself in the card, and in a delegated run with no viewer of
// its own it asks the chat that started it.
func TestAMarkedToolAsksTheWatchingChat(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	SetUserToolAsksInChat(AuthDB(), "alice", "a-research", "web_search", true)
	var asked, detail string
	sub := &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a-helper", Owner: "alice"},
		askChain: []string{"a-research"},
		askVia: func(prompt, d string, yes []string) string {
			asked, detail = prompt, d
			return yes[0]
		}}
	confirm := sub.confirmFuncFor(&ToolSession{Username: "alice"})
	if !confirm("web_search", `{"query":"best espresso grinder"}`) || !strings.Contains(asked, "web_search") {
		t.Fatalf("the marked search did not ask: %q", asked)
	}
	if !strings.Contains(detail, "espresso") {
		t.Errorf("the card does not show the query: %q", detail)
	}
	asked = ""
	if !confirm("get_weather", `{}`) || asked != "" {
		t.Errorf("an unmarked tool asked: %q", asked)
	}
}

// On an agent's Tools section a tool that asks everywhere reads as Ask and
// cannot be turned off there.
func TestAnEverywhereAskIsLockedOnTheAgent(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a11", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	SetUserToolAsksInChat(AuthDB(), "alice", "", "web_search", true)
	r := httptest.NewRequest(http.MethodPatch,
		"/api/agent-access/tool?agent=a11&name=web_search", strings.NewReader(`{"chat":"allow"}`))
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, "alice"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "every agent") {
		t.Errorf("turning off an everywhere ask on one agent: %d %s", w.Code, w.Body.String())
	}
	if !UserToolAsksInChat(AuthDB(), "alice", "a11", "web_search") {
		t.Error("the agent stopped asking")
	}
}

// "Allow for this session" stops the asking for that tool in that
// conversation, a run it hands over included; "Allow once" does not; another
// conversation still asks.
func TestAllowForThisSessionCoversTheConversation(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	SetUserToolAsksInChat(AuthDB(), "alice", "a-research", "web_search", true)
	answer := "Allow for this session"
	asked := 0
	ask := func(prompt, d string, yes []string) string {
		asked++
		for _, y := range yes {
			if y == answer {
				return y
			}
		}
		return ""
	}
	turn := func(conv string) *chatTurn {
		return &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a-helper", Owner: "alice"},
			askChain: []string{"a-research"}, askConversation: conv, askVia: ask}
	}
	sess := &ToolSession{Username: "alice"}

	answer = "Allow once"
	c := turn("conv-1").confirmFuncFor(sess)
	c("web_search", `{"query":"a"}`)
	c("web_search", `{"query":"b"}`)
	if asked != 2 {
		t.Fatalf("Allow once covered a later call: asked %d", asked)
	}

	answer = "Allow for this session"
	c("web_search", `{"query":"c"}`)
	if !c("web_search", `{"query":"d"}`) || asked != 3 {
		t.Errorf("allowed for the session, still asked: %d", asked)
	}
	if !turn("conv-1").confirmFuncFor(sess)("web_search", `{"query":"e"}`) || asked != 3 {
		t.Errorf("another run in the same conversation asked again: %d", asked)
	}
	turn("conv-2").confirmFuncFor(sess)("web_search", `{"query":"f"}`)
	if asked != 4 {
		t.Errorf("another conversation did not ask: %d", asked)
	}
}
