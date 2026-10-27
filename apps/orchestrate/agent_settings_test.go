package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A bool cannot hold "not decided here". The difference between "off because
// the default is off" and "off because I set it" only appears later, when the
// default changes: one agent should follow and the other should not, and
// nothing recorded which was which.

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
	for _, want := range []string{"Default:", "Allowed", "has not decided"} {
		if !strings.Contains(got, want) {
			t.Errorf("an inheriting agent's line is missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, settingOn) {
		t.Errorf("the line prints the stored value instead of the word: %q", got)
	}
	setDeploymentSetting(RootDB, deploymentDefault, defaultWorkspaceNetwork, settingOff)
	if got = workspaceNetworkSource(RootDB, rec); !strings.Contains(got, "Default: Blocked") {
		t.Errorf("an undecided agent does not say what it is following: %q", got)
	}
	// An override reads as one, and still names the default it is departing
	// from - which is the fact that decides whether to keep the override.
	rec.WorkspaceNetwork = settingOn
	got = workspaceNetworkSource(RootDB, rec)
	if !strings.Contains(got, "Set on this agent: Allowed") {
		t.Errorf("an override does not read as one: %q", got)
	}
	if !strings.Contains(got, "Default: Blocked") {
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
		routed(app)(w, asUser(r, "alice"))
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

// "Consult the Lead" is its own per-agent setting. It used to ride along with
// the authoring toolset, so only authoring agents had it, and dispatched runs
// (which build that toolset with no turn) got a copy that could only fail.

func TestConsultTheLeadResolves(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = prev })

	plain := AgentRecord{ID: "a1"}
	author := AgentRecord{ID: "seed-builder"}
	if settingIsOn(db, plain, defaultConsultLead) {
		t.Error("an ordinary agent does not consult by default")
	}
	if !settingIsOn(db, author, defaultConsultLead) {
		t.Error("Builder, the authoring agent, keeps consulting, as it always has")
	}
	author.ConsultLead = settingOff
	if settingIsOn(db, author, defaultConsultLead) {
		t.Error("an explicit Off beats the old authoring default")
	}
	plain.ConsultLead = settingOn
	if !settingIsOn(db, plain, defaultConsultLead) {
		t.Error("any agent can be switched on")
	}
	// An administrator's limit of Off holds everybody on the worker.
	setDeploymentSetting(db, deploymentMaximum, defaultConsultLead, settingOff)
	if settingIsOn(db, plain, defaultConsultLead) || settingIsOn(db, AgentRecord{ID: "a3", Author: true}, defaultConsultLead) {
		t.Error("the deployment limit keeps every agent from consulting")
	}
}

func TestTheAuthoringToolsNoLongerCarryConsult(t *testing.T) {
	for _, td := range builderAuthoringTools(&ToolSession{Username: "u"}, nil) {
		if td.Tool.Name == "consult" {
			t.Fatal("consult rides its own setting now; a dispatched run's copy could never work")
		}
	}
}

// One choice over two stored fields: each choice stores what it means, an
// agent following the default stays following it when the form sends the
// choice back unchanged, and the old fourth combination reads as the lead.
func TestTheLeadChoiceRoundTrips(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })

	plain := AgentRecord{ID: "a1", Name: "Plain"} // no authoring: default is off
	if got := leadUseOf(RootDB, plain); got != leadUseOff {
		t.Fatalf("an undecided plain agent reads %q", got)
	}
	a := plain
	applyLeadUse(RootDB, &a, leadUseOff)
	if a.ConsultLead != "" || a.LeadModel {
		t.Errorf("choosing what it already has pinned it: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseConsult)
	if a.ConsultLead != settingOn || a.LeadModel || leadUseOf(RootDB, a) != leadUseConsult {
		t.Errorf("consult: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseLead)
	if !a.LeadModel || leadUseOf(RootDB, a) != leadUseLead {
		t.Errorf("lead: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseOff)
	if a.LeadModel || a.ConsultLead != "" || leadUseOf(RootDB, a) != leadUseOff {
		t.Errorf("back to off should follow the default again: %+v", a)
	}

	// Planning on the lead is the lead with LeadPlans; leaving it for any
	// other choice clears LeadPlans.
	applyLeadUse(RootDB, &a, leadUsePlan)
	if !a.LeadModel || !a.LeadPlans || leadUseOf(RootDB, a) != leadUsePlan {
		t.Errorf("plan: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseLead)
	if !a.LeadModel || a.LeadPlans || leadUseOf(RootDB, a) != leadUseLead {
		t.Errorf("plan back to lead: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUsePlan)
	applyLeadUse(RootDB, &a, leadUseOff)
	if a.LeadModel || a.LeadPlans {
		t.Errorf("plan to off: %+v", a)
	}
	// LeadPlans without LeadModel is inert and reads as what it is.
	if got := leadUseOf(RootDB, AgentRecord{ID: "a3", LeadPlans: true}); got == leadUsePlan {
		t.Error("LeadPlans alone reads as planning on the lead")
	}
	// On Builder the choice means its own thing, the lead designing apps and a
	// worker writing them (builder_app_split.go), and is named for it.
	for _, o := range leadUseField(true, AgentRecord{ID: "seed-builder"}).Options {
		if o.Value == leadUsePlan && !strings.Contains(o.Label, "Lead designs apps") {
			t.Errorf("Builder's plan choice reads %q", o.Label)
		}
	}

	// The fourth combination (lead on and consult on) is the lead.
	both := AgentRecord{ID: "a2", LeadModel: true, ConsultLead: settingOn}
	if got := leadUseOf(RootDB, both); got != leadUseLead {
		t.Errorf("lead + consult reads %q", got)
	}

	// A scale from the worker alone to the lead for everything, the default
	// said in its stop's own help line.
	f := leadUseField(true, plain)
	var order []string
	for _, o := range f.Options {
		order = append(order, o.Value)
	}
	if f.Field != "lead_use" || f.Type != "scale" || strings.Join(order, " ") != "off consult review plan lead" ||
		!strings.Contains(f.Options[0].Help, "This is the default.") {
		t.Errorf("the scale: %+v", f.Options)
	}
	if m := withEditorChoices(RootDB, plain); string(m["lead_use"]) != `"off"` {
		t.Errorf("the form's lead_use: %s", m["lead_use"])
	}
}

// The lead choice saves through PATCH, which is how the editor sends it, for
// every value it offers: the translation writes lead_plans as well as
// lead_model and consult_lead, and a field the allowlist did not know made
// "Lead plans" (and Builder's "Lead designs apps") answer 400.
func TestTheLeadChoiceSavesThroughPATCH(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	for _, id := range []string{"seed-builder", ""} {
		a, err := saveAgent(udb, AgentRecord{ID: id, Owner: user, Name: "Agent " + id, OrchestratorPrompt: "Help."})
		if err != nil {
			t.Fatal(err)
		}
		for _, choice := range []string{leadUsePlan, leadUseLead, leadUseReview, leadUseOff, leadUseReview, leadUsePlan} {
			w := httptest.NewRecorder()
			body := `{"id":"` + a.ID + `","lead_use":"` + choice + `"}`
			routed(T)(w, as(t, httptest.NewRequest(http.MethodPatch, "/api/agents?id="+a.ID, strings.NewReader(body)), user))
			if w.Code != http.StatusOK {
				t.Fatalf("%s: lead_use=%s answered %d: %s", a.ID, choice, w.Code, w.Body.String())
			}
			got, _ := loadAgent(udb, a.ID)
			if leadUseOf(RootDB, got) != choice && !(choice == leadUseOff && leadUseOf(RootDB, got) == leadUseDefault(RootDB, got)) {
				t.Errorf("%s: chose %s, reads %s", a.ID, choice, leadUseOf(RootDB, got))
			}
		}
	}
}

// The editor's Reasoning section had Think mode and Effort, and a set Effort
// decides Think, so Effort Off and Think Off were the same control twice. It
// is one choice now, over the same two stored fields: Default, Off, or a
// level. A forced On with no level was Default unless the admin turned the
// route off, so it reads as Default and saves as following the route.

func TestTheReasoningChoiceReadsAsTheAgentRuns(t *testing.T) {
	for _, c := range []struct {
		think, effort, want string
	}{
		{"", "", ""},
		{"on", "", ""},
		{"off", "", "off"},
		{"off", "high", "high"}, // a set effort wins, as thinkMode has it
		{"on", "off", "off"},
	} {
		if got := reasoningOf(AgentRecord{Think: c.think, Effort: c.effort}); got != c.want {
			t.Errorf("think=%q effort=%q reads %q, want %q", c.think, c.effort, got, c.want)
		}
	}

	// A form sending the choice back with the fields it loaded runs as it
	// did, and a value that is no choice changes nothing.
	a := AgentRecord{Think: "off", Effort: "high"}
	applyEditorChoices(nil, &a, map[string]json.RawMessage{"reasoning": json.RawMessage(`"high"`)})
	if a.thinkMode() != "on" || a.Effort != "high" {
		t.Errorf("an unchanged choice changed how it runs: %+v", a)
	}
	applyEditorChoices(nil, &a, map[string]json.RawMessage{"reasoning": json.RawMessage(`"bogus"`)})
	if a.Effort != "high" {
		t.Errorf("a value that is no choice changed the record: %+v", a)
	}

	if m := withEditorChoices(nil, AgentRecord{Think: "off"}); string(m["reasoning"]) != `"off"` {
		t.Errorf("the form's reasoning: %s", m["reasoning"])
	}
}

func TestTheReasoningChoiceSavesThroughPATCH(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Agent", OrchestratorPrompt: "Help.", Think: "on"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ choice, mode, effort string }{
		{"", "", ""}, // a forced on saved as Default follows the route
		{"off", "off", "off"},
		{"high", "on", "high"},
		{"low", "on", "low"},
		{"", "", ""},
	} {
		w := httptest.NewRecorder()
		body := `{"reasoning":"` + c.choice + `"}`
		routed(T)(w, as(t, httptest.NewRequest(http.MethodPatch, "/api/agents?id="+a.ID, strings.NewReader(body)), user))
		if w.Code != http.StatusOK {
			t.Fatalf("reasoning=%q answered %d: %s", c.choice, w.Code, w.Body.String())
		}
		got, _ := loadAgent(udb, a.ID)
		if got.thinkMode() != c.mode || got.Effort != c.effort || reasoningOf(got) != c.choice {
			t.Errorf("chose %q: think=%q effort=%q runs %q", c.choice, got.Think, got.Effort, got.thinkMode())
		}
	}
}
