package orchestrate

// A bool cannot hold "not decided here". The difference between "off because
// the default is off" and "off because I set it" only appears later, when the
// default changes: one agent should follow and the other should not, and
// nothing recorded which was which.

import (
	"net/http"
	"net/http/httptest"
	"slices"
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
	if !agentWorkspaceNetwork(RootDB, undecided) {
		t.Error("an agent with nothing set anywhere was blocked")
	}
	// The deployment's default reaches it.
	setDeploymentSetting(RootDB, deploymentDefault, defaultWorkspaceNetwork, settingOff)
	if agentWorkspaceNetwork(RootDB, undecided) {
		t.Error("the default for all agents did not reach an undecided agent")
	}
	// Its own answer wins, in EITHER direction. A default that could only
	// tighten would forbid "no agent reaches the network except this one".
	allowed := undecided
	allowed.WorkspaceNetwork = settingOn
	if !agentWorkspaceNetwork(RootDB, allowed) {
		t.Error("an agent could not be looser than the default")
	}
	// Clearing the default is NOT setting it off: cleared, the undecided
	// agent goes back to the framework's answer.
	setDeploymentSetting(RootDB, deploymentDefault, defaultWorkspaceNetwork, "")
	if !agentWorkspaceNetwork(RootDB, undecided) {
		t.Error("clearing the default left it blocking")
	}
}

// A record written before the tri-state still means what it meant. It could
// only ever record a BLOCK, so it is read as one.
func TestALegacyBlockIsStillABlock(t *testing.T) {
	_, _, _ = newTestOrchestrate(t)
	pinRootDB(t)
	old := AgentRecord{ID: "a", Owner: "alice", WorkspaceNoNetwork: true}
	if agentWorkspaceNetwork(RootDB, old) {
		t.Error("an agent blocked before the tri-state existed started reaching the network")
	}
	// And a fresh answer overrides it, or the migration would be a trap: the
	// owner sets Allowed, the stale bool keeps blocking, and nothing on screen
	// explains why.
	fresh := old
	fresh.WorkspaceNetwork = settingOn
	if !agentWorkspaceNetwork(RootDB, fresh) {
		t.Error("a stale block outranked a fresh allow")
	}
}

// The page has to show an override AS an override, which is what makes a
// default safe to be looser than a ceiling would allow.
func TestThePageSaysWhereTheAnswerCameFrom(t *testing.T) {
	_, _, _ = newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a", Owner: "alice"}
	// An agent that has decided nothing leads with the DEFAULT and the value
	// it resolves to, in the setting's own words. It used to open "Currently"
	// and then name the rung the answer came from, which buried both halves:
	// "currently" is true of every value a control ever shows, and the value
	// arrived last in its stored spelling.
	got := workspaceNetworkSource(RootDB, rec)
	for _, want := range []string{"Default Setting:", "Allowed", "has not decided"} {
		if !strings.Contains(got, want) {
			t.Errorf("an inheriting agent's line is missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, settingOn) {
		t.Errorf("the line prints the stored value instead of the word: %q", got)
	}
	setDeploymentSetting(RootDB, deploymentDefault, defaultWorkspaceNetwork, settingOff)
	if got = workspaceNetworkSource(RootDB, rec); !strings.Contains(got, "Default Setting: Blocked") {
		t.Errorf("an undecided agent does not say what it is following: %q", got)
	}
	// An override reads as one, and still names the default it is departing
	// from - which is the fact that decides whether to keep the override.
	rec.WorkspaceNetwork = settingOn
	got = workspaceNetworkSource(RootDB, rec)
	if !strings.Contains(got, "Set on this agent: Allowed") {
		t.Errorf("an override does not read as one: %q", got)
	}
	if !strings.Contains(got, "The default is Blocked") {
		t.Errorf("an override does not say what it departs from: %q", got)
	}
}

// Only NAMED settings are accepted. An open key-value store would be a page
// where a typo silently creates a default nothing reads, which looks exactly
// like one that works.
func TestDeploymentSettingsRefuseWhatNothingReads(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	// These are ADMIN controls, so the harness's user has to be one. Without
	// this every call below is a 403 and the test proves the gate rather than
	// the validation it was written for.
	AuthDB().Set(AuthTable, "user:alice", AuthUser{Username: "alice", Admin: true})
	patch := func(body string) int {
		r := httptest.NewRequest(http.MethodPatch, "/api/console/deployment-settings", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleDeploymentSettings(w, asUser(r, "alice"))
		return w.Code
	}
	if code := patch(`{"workspace_network":"off"}`); code != http.StatusNoContent {
		t.Fatalf("setting a known default: %d", code)
	}
	if deploymentSetting(RootDB, deploymentDefault, defaultWorkspaceNetwork) != settingOff {
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
	if deploymentSetting(RootDB, deploymentDefault, "workspce_netwrk") != "" {
		t.Error("a key nothing reads was stored, so it looks like a setting that works")
	}
}

// Every settable default is in the table, and every entry is complete. A
// setting missing its framework answer defaults to the empty string, which for
// an on/off setting is neither and behaves as off - a silent tightening on a
// deployment that set nothing.
func TestEveryDefaultableSettingIsWiredWholly(t *testing.T) {
	for key, s := range triSettings {
		if s.key != key {
			t.Errorf("%s is filed under the wrong key: %q", key, s.key)
		}
		if s.own == nil || s.legacy == nil {
			t.Errorf("%s cannot be read", key)
		}
		if s.framework == "" && key != defaultInboundMode {
			t.Errorf("%s has no answer for a deployment that set nothing", key)
		}
		if len(s.values) == 0 {
			t.Errorf("%s accepts nothing, so its control can never be set", key)
		}
		// The framework answer must be one the setting accepts, or the
		// resolver returns a value the writer would have refused.
		if s.framework != "" && !slices.Contains(s.values, s.framework) {
			t.Errorf("%s defaults to %q, which it does not accept", key, s.framework)
		}
	}
	// The ones that exist, so adding a field without wiring it shows up here
	// rather than as a control that saves and does nothing.
	for _, want := range []string{
		defaultWorkspaceNetwork, defaultShareCortex, defaultShareReference,
		defaultShareNotes, defaultShareUploads, defaultInboundMode,
	} {
		if _, ok := triSettings[want]; !ok {
			t.Errorf("%s is not in the table", want)
		}
	}
}

// The share layers each keep the answer they had before defaults existed. A
// deployment that sets nothing must behave exactly as it did.
func TestTheShareLayersKeepTheirOldDefaults(t *testing.T) {
	_, _, _ = newTestOrchestrate(t)
	pinRootDB(t)
	plain := AgentRecord{ID: "a", Owner: "alice"}
	for key, want := range map[string]bool{
		defaultShareCortex:    true,  // travelled before
		defaultShareReference: true,  // travelled before
		defaultShareNotes:     false, // did NOT: it granted rather than withheld
		defaultShareUploads:   true,  // recipients could add documents
	} {
		if got := settingIsOn(RootDB, plain, key); got != want {
			t.Errorf("%s changed for an agent that set nothing: got %v want %v", key, got, want)
		}
	}
	// And a legacy record still says what it said. share_memory_explicit is
	// the one stored POSITIVELY: a true there means on, where the others mean
	// off, and reading it the same way as its neighbours would invert it.
	old := AgentRecord{ID: "a", Owner: "alice", ShareMemoryExplicit: true, ShareHoldCortex: true}
	if !settingIsOn(RootDB, old, defaultShareNotes) {
		t.Error("an agent that shared its notes stopped sharing them")
	}
	if settingIsOn(RootDB, old, defaultShareCortex) {
		t.Error("an agent holding its cortex back started sharing it")
	}
}
