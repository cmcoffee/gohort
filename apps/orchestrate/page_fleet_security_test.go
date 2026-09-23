package orchestrate

// What holds for EVERY agent, in one place. The per-agent page answers "what
// can this one do" and cannot answer "what is true of all of them", so an
// owner with a dozen agents visited a dozen pages to find out, or to change
// one thing everywhere.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The fleet page shows ONLY what binds every agent. An agent's own decision
// on a page about all of them is the confusion the per-agent page was split to
// end, arriving from the other side.
func TestTheFleetPageShowsOnlyFleetWideDecisions(t *testing.T) {
	src := mustRead(t, "page_fleet_security.go")
	if !strings.Contains(src, `"&scope=fleet"`) {
		t.Error("the fleet page does not narrow to fleet-wide rows")
	}
	// Requests are the exception and deliberately unscoped: a run that has
	// stopped is waiting whichever agent it belongs to.
	i := strings.Index(src, `kind=requests`)
	if i < 0 {
		t.Fatal("the fleet page cannot answer what is waiting")
	}
	if strings.Contains(src[i:i+80], "scope=fleet") {
		t.Error("pending requests are scoped to fleet-wide rows, so most would be invisible")
	}
}

// Reachable from an agent, and not an agent itself. "all" is checked before
// anything is looked up, so an agent cannot shadow it.
func TestTheFleetPageIsReachableAndIsNotAnAgent(t *testing.T) {
	route := mustRead(t, "page_agent.go")
	i := strings.Index(route, "fleetSecurityID")
	j := strings.Index(route, `action == "access" && id != ""`)
	if i < 0 || j < 0 {
		t.Fatal("the access route has moved")
	}
	if i > j {
		t.Error("the fleet id is checked after the agent lookup, so an agent named all would shadow it")
	}
	if !strings.Contains(mustRead(t, "page_agent_access.go"), "fleetSecurityID") {
		t.Error("no agent page links to the defaults it reads, so they cannot be found")
	}
}

// A value set for every agent reaches an agent that has decided nothing, and
// does NOT overwrite one that has. That is what makes it a default rather than
// a ceiling.
func TestAFleetValueIsReadOnlyWhereAnAgentHasNotDecided(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	// Ask-marks: agent-first, then fleet.
	SetUserToolAsksInChat(udb, "alice", "", "web_search", true)
	if !UserToolAsksInChat(udb, "alice", "quiet_agent", "web_search") {
		t.Error("an agent that decided nothing does not read the fleet value")
	}
	// Contacts: the same shape, through a different store.
	SetContactPolicy(RootDB, "alice", "", "ops@example.test", PolicyAllow)
	if got := ContactPolicy(RootDB, "alice", "any_agent", "ops@example.test"); got != PolicyAllow {
		t.Errorf("the fleet contact policy did not reach an undecided agent: %q", got)
	}
	// And an agent's OWN answer wins, in either direction. A default that
	// could only tighten would forbid "no agent reaches this except one".
	SetContactPolicy(RootDB, "alice", "loud_agent", "ops@example.test", PolicyBlock)
	if got := ContactPolicy(RootDB, "alice", "loud_agent", "ops@example.test"); got != PolicyBlock {
		t.Errorf("an agent's own decision lost to the fleet default: %q", got)
	}
}

// The page renders for a user with no decisions at all, which is every user
// before they make one. An empty security page must say so rather than error.
func TestTheFleetPageRendersWithNothingSet(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	r := httptest.NewRequest(http.MethodGet, "/agent/all/access", nil)
	w := httptest.NewRecorder()
	app.renderFleetSecurity(w, asUser(r, "alice"), "alice", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("the fleet page does not render: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "all agents") {
		t.Error("the page does not say what it is about")
	}
}
