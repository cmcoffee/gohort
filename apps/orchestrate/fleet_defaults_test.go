package orchestrate

// A bool cannot hold "not decided here". The difference between "off because
// the default is off" and "off because I set it" only appears later, when the
// default changes: one agent should follow and the other should not, and
// nothing recorded which was which.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The four answers, in the order they override each other.
func TestWorkspaceReachResolvesInOrder(t *testing.T) {
	_, _, _ = newTestOrchestrate(t)
	pinRootDB(t)
	undecided := AgentRecord{ID: "a", Owner: "alice"}

	// Nothing set anywhere: the framework's own answer, which is OPEN. Every
	// deployment did this before any of it, and must keep doing it.
	if !agentWorkspaceNetwork(RootDB, "alice", undecided) {
		t.Error("an agent with nothing set anywhere was blocked")
	}
	// The owner's default reaches it.
	setFleetDefault(RootDB, "alice", defaultWorkspaceNetwork, settingOff)
	if agentWorkspaceNetwork(RootDB, "alice", undecided) {
		t.Error("the default for all agents did not reach an undecided agent")
	}
	// Its own answer wins, in EITHER direction. A default that could only
	// tighten would forbid "no agent reaches the network except this one".
	allowed := undecided
	allowed.WorkspaceNetwork = settingOn
	if !agentWorkspaceNetwork(RootDB, "alice", allowed) {
		t.Error("an agent could not be looser than the default")
	}
	// Clearing the default is NOT setting it off: cleared, the undecided
	// agent goes back to the framework's answer.
	setFleetDefault(RootDB, "alice", defaultWorkspaceNetwork, "")
	if !agentWorkspaceNetwork(RootDB, "alice", undecided) {
		t.Error("clearing the default left it blocking")
	}
}

// A record written before the tri-state still means what it meant. It could
// only ever record a BLOCK, so it is read as one.
func TestALegacyBlockIsStillABlock(t *testing.T) {
	_, _, _ = newTestOrchestrate(t)
	pinRootDB(t)
	old := AgentRecord{ID: "a", Owner: "alice", WorkspaceNoNetwork: true}
	if agentWorkspaceNetwork(RootDB, "alice", old) {
		t.Error("an agent blocked before the tri-state existed started reaching the network")
	}
	// And a fresh answer overrides it, or the migration would be a trap: the
	// owner sets Allowed, the stale bool keeps blocking, and nothing on screen
	// explains why.
	fresh := old
	fresh.WorkspaceNetwork = settingOn
	if !agentWorkspaceNetwork(RootDB, "alice", fresh) {
		t.Error("a stale block outranked a fresh allow")
	}
}

// The page has to show an override AS an override, which is what makes a
// default safe to be looser than a ceiling would allow.
func TestThePageSaysWhereTheAnswerCameFrom(t *testing.T) {
	_, _, _ = newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a", Owner: "alice"}
	if got := workspaceNetworkSource(RootDB, "alice", rec); !strings.Contains(got, "default for all agents") {
		t.Errorf("an undecided agent does not say it is inheriting: %q", got)
	}
	rec.WorkspaceNetwork = settingOff
	if got := workspaceNetworkSource(RootDB, "alice", rec); !strings.Contains(got, "set on this agent") {
		t.Errorf("an override does not read as one: %q", got)
	}
}

// Only NAMED settings are accepted. An open key-value store would be a page
// where a typo silently creates a default nothing reads, which looks exactly
// like one that works.
func TestFleetDefaultsRefuseWhatNothingReads(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	patch := func(body string) int {
		r := httptest.NewRequest(http.MethodPatch, "/api/console/fleet-defaults", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleFleetDefaults(w, asUser(r, "alice"))
		return w.Code
	}
	if code := patch(`{"workspace_network":"off"}`); code != http.StatusNoContent {
		t.Fatalf("setting a known default: %d", code)
	}
	if fleetDefault(RootDB, "alice", defaultWorkspaceNetwork) != settingOff {
		t.Error("the default did not stick")
	}
	// A value the resolver does not know reads as SOME state and behaves as
	// none.
	if code := patch(`{"workspace_network":"sometimes"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown value was stored: %d", code)
	}
	// A key nothing reads is ignored rather than stored.
	if code := patch(`{"workspce_netwrk":"off"}`); code != http.StatusNoContent {
		t.Errorf("a misspelled key errored instead of being ignored: %d", code)
	}
	if fleetDefault(RootDB, "alice", "workspce_netwrk") != "" {
		t.Error("a key nothing reads was stored, so it looks like a setting that works")
	}
}
