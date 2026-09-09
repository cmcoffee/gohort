package orchestrate

import (
	"reflect"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func overlayTestDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

// seedForOverlayTest returns a seed and a shadow of it, the way the world
// actually produces one: a user approves a tool, and the approval path writes
// a record at the seed's ID.
func seedForOverlayTest(t *testing.T) (AgentRecord, AgentRecord) {
	t.Helper()
	seed, ok := seedAgentByID("seed-research")
	if !ok {
		t.Fatal("seed-research is gone")
	}
	shadow := seed
	shadow.Owner = "craig@example.com"
	return seed, shadow
}

// THE BUG the overlay exists for. A shadow used to win entirely, so the first
// thing that wrote one froze every other field at that instant and no
// framework improvement reached that user again. The fix was a hand-written
// list of fields to refresh, which covered only the ones somebody had already
// noticed had frozen.
func TestUntouchedFieldsTrackTheSeed(t *testing.T) {
	db := overlayTestDB(t)
	seed, shadow := seedForOverlayTest(t)

	// The user decides ONE thing: a tighter round budget.
	shadow.MaxWorkerRounds = 4
	if _, err := saveAgent(db, shadow); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok := loadAgent(db, seed.ID)
	if !ok {
		t.Fatal("the shadow did not load")
	}
	if got.MaxWorkerRounds != 4 {
		t.Errorf("the user's round budget did not stick: %d", got.MaxWorkerRounds)
	}
	if !hasField(got.OverriddenFields, "max_worker_rounds") {
		t.Errorf("the field they changed was not recorded: %v", got.OverriddenFields)
	}
	// allowed_tools may also appear: selfHealAllowedTools drops tools this
	// store does not have registered, and that narrowing is recorded as a
	// deployment decision on purpose (see agents_mode.go). Nothing else
	// should be there.
	for _, f := range got.OverriddenFields {
		if f != "max_worker_rounds" && f != "allowed_tools" {
			t.Errorf("%q was recorded as a user decision and was not one", f)
		}
	}
	// Everything else still comes from the framework. GapCheck is the stand-in
	// for every field nobody has thought about: the user never expressed a
	// view, so a change to the seed has to reach them.
	if got.GapCheck != seed.GapCheck || got.MaxPlanSteps != seed.MaxPlanSteps {
		t.Errorf("untouched fields froze: gap_check=%v plan_steps=%d", got.GapCheck, got.MaxPlanSteps)
	}
	if got.OrchestratorPrompt != seed.OrchestratorPrompt || got.Rules != seed.Rules {
		t.Error("the framework's prompt or rules did not reach a shadowed user")
	}
}

// A seed that moves must reach the fields the user never claimed, and must not
// touch the one they did.
func TestASeedChangeReachesEveryFieldButTheirs(t *testing.T) {
	db := overlayTestDB(t)
	seed, shadow := seedForOverlayTest(t)
	shadow.MaxWorkerRounds = 4
	if _, err := saveAgent(db, shadow); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The framework ships a new budget and a new plan guidance.
	next := seed
	next.MaxWorkerRounds = 99
	next.MaxPlanSteps = seed.MaxPlanSteps + 3
	next.PlanGuidance = "Decompose differently."

	var stored AgentRecord
	if !db.Get(agentsTable, seed.ID, &stored) {
		t.Fatal("no stored shadow")
	}
	got := resolveSeedShadow(next, stored)

	if got.MaxWorkerRounds != 4 {
		t.Errorf("the framework overwrote a field the user set: %d", got.MaxWorkerRounds)
	}
	if got.MaxPlanSteps != next.MaxPlanSteps {
		t.Errorf("a new plan-step budget did not reach the user: %d", got.MaxPlanSteps)
	}
	if got.PlanGuidance != next.PlanGuidance {
		t.Error("new plan guidance did not reach the user")
	}
}

// A shadow written before overlays carries no list, so its differences are the
// only evidence of intent. Reading it that way leaves the user seeing exactly
// what they saw yesterday.
func TestLegacyShadowKeepsEveryDifferenceItHad(t *testing.T) {
	seed, shadow := seedForOverlayTest(t)
	shadow.MaxWorkerRounds = 4
	shadow.Hidden = !seed.Hidden
	shadow.Rules = "Only answer on Tuesdays."
	shadow.OverriddenFields = nil // legacy: nobody recorded anything
	shadow.OverlayRev = 0

	got := resolveSeedShadow(seed, shadow)
	if got.MaxWorkerRounds != 4 || got.Hidden != !seed.Hidden || got.Rules != "Only answer on Tuesdays." {
		t.Errorf("a legacy shadow lost a difference: rounds=%d hidden=%v rules=%q",
			got.MaxWorkerRounds, got.Hidden, got.Rules)
	}
}

// And why that inference has to happen ONCE, at upgrade, rather than on every
// read. Diffing a legacy shadow against the CURRENT seed cannot tell a field
// the user edited from a field the framework changed afterwards, so read-time
// inference would keep re-freezing. migrateSeedShadowOverlays fixes the
// comparison at the last moment the two are still in step.
func TestStampingALegacyShadowEndsTheFreeze(t *testing.T) {
	seed, shadow := seedForOverlayTest(t)
	shadow.MaxWorkerRounds = 4 // the user's one decision
	shadow.OverlayRev = 0

	// What the migration does.
	shadow.OverriddenFields = agentOverrides(seed, shadow)
	shadow.OverlayRev = 1
	if !reflect.DeepEqual(shadow.OverriddenFields, []string{"max_worker_rounds"}) {
		t.Fatalf("the migration recorded %v", shadow.OverriddenFields)
	}

	// The framework then moves a field the user never touched.
	next := seed
	next.MaxPlanSteps = seed.MaxPlanSteps + 5
	got := resolveSeedShadow(next, shadow)
	if got.MaxPlanSteps != next.MaxPlanSteps {
		t.Errorf("a stamped shadow still froze an untouched field: %d", got.MaxPlanSteps)
	}
	if got.MaxWorkerRounds != 4 {
		t.Errorf("the user's decision was lost: %d", got.MaxWorkerRounds)
	}
}

// Revert is free: put a field back to the framework's value and it stops being
// an override, so the next improvement to it lands.
func TestMatchingTheDefaultDropsTheOverride(t *testing.T) {
	db := overlayTestDB(t)
	seed, shadow := seedForOverlayTest(t)
	shadow.MaxWorkerRounds = 4
	saved, err := saveAgent(db, shadow)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !hasField(saved.OverriddenFields, "max_worker_rounds") {
		t.Fatalf("the override was not recorded: %v", saved.OverriddenFields)
	}

	saved.MaxWorkerRounds = seed.MaxWorkerRounds
	saved, err = saveAgent(db, saved)
	if err != nil {
		t.Fatalf("resave: %v", err)
	}
	if hasField(saved.OverriddenFields, "max_worker_rounds") {
		t.Errorf("reverting a field left it overridden: %v", saved.OverriddenFields)
	}
}

// The framework keeps a short list for itself: what the agent IS, rather than
// how this deployment tuned it. A shadow cannot claim any of it.
func TestFrameworkOwnedFieldsCannotBeOverridden(t *testing.T) {
	db := overlayTestDB(t)
	seed, shadow := seedForOverlayTest(t)

	shadow.OrchestratorPrompt = "You are something else entirely."
	shadow.Description = "Not what the framework says."
	shadow.Cortex = !seed.Cortex
	shadow.Fleet = !seed.Fleet
	shadow.PreMortem = !seed.PreMortem
	saved, err := saveAgent(db, shadow)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, name := range saved.OverriddenFields {
		if frameworkOwnedSeedFields(seed.ID)[name] {
			t.Errorf("%q was recorded as a user override", name)
		}
	}

	got, _ := loadAgent(db, seed.ID)
	if got.OrchestratorPrompt != seed.OrchestratorPrompt {
		t.Error("a shadow replaced the framework's prompt")
	}
	if got.Description != seed.Description {
		t.Error("a shadow replaced the framework's description")
	}
	if got.Cortex != seed.Cortex || got.Fleet != seed.Fleet || got.PreMortem != seed.PreMortem {
		t.Error("a shadow changed the agent's type flags, which decide its thread and its toolset")
	}
}

// Identity is never inherited: the record is the user's, whatever it wears.
func TestOverlayKeepsTheRecordsOwnIdentity(t *testing.T) {
	seed, shadow := seedForOverlayTest(t)
	shadow.Owner = "craig@example.com"
	got := resolveSeedShadow(seed, shadow)
	if got.ID != seed.ID {
		t.Errorf("id = %q", got.ID)
	}
	if got.Owner != "craig@example.com" {
		t.Errorf("owner = %q, want the user who owns the shadow", got.Owner)
	}
}

// A save then a load must produce what the caller saved, or every editor in
// the app silently loses a field.
func TestSeedShadowRoundTrips(t *testing.T) {
	db := overlayTestDB(t)
	_, shadow := seedForOverlayTest(t)
	shadow.Rules = "Cite everything."
	shadow.MaxWorkerRounds = 7
	shadow.MaxPlanSteps = 2
	shadow.AllowedTools = []string{"web_search"}
	shadow.LeadModel = true
	shadow.Exposed = false
	shadow.Hidden = true

	saved, err := saveAgent(db, shadow)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadAgent(db, saved.ID)
	if !ok {
		t.Fatal("not found after save")
	}
	if got.Rules != shadow.Rules || got.MaxWorkerRounds != 7 || got.MaxPlanSteps != 2 ||
		!got.LeadModel || !got.Hidden {
		t.Errorf("round trip lost a decision: rules=%q rounds=%d steps=%d lead=%v hidden=%v",
			got.Rules, got.MaxWorkerRounds, got.MaxPlanSteps, got.LeadModel, got.Hidden)
	}
	// The tool list is a decision too, even though selfHealAllowedTools
	// rewrites the value itself when the named tools are not registered in
	// this store. What matters here is that narrowing the list was recorded as
	// the user's, so a wider seed list never silently reopens it.
	if !hasField(got.OverriddenFields, "allowed_tools") {
		t.Errorf("narrowing the tool list was not recorded: %v", got.OverriddenFields)
	}
}

func hasField(fields []string, name string) bool {
	for _, f := range fields {
		if f == name {
			return true
		}
	}
	return false
}

// Loading an agent must not write to the store. The tool-list self-heal
// persists what it heals, and with the seed as the base a seed list naming a
// tool this deployment lacks would be healed, dropped on the next read,
// healed again, and written again: one store write per read of the agent.
func TestLoadingASeedShadowTwiceDoesNotKeepWriting(t *testing.T) {
	db := overlayTestDB(t)
	_, shadow := seedForOverlayTest(t)
	shadow.Rules = "Cite everything."
	if _, err := saveAgent(db, shadow); err != nil {
		t.Fatalf("save: %v", err)
	}

	first, ok := loadAgent(db, shadow.ID)
	if !ok {
		t.Fatal("not found")
	}
	second, _ := loadAgent(db, shadow.ID)
	if !first.Updated.Equal(second.Updated) {
		t.Errorf("reading the agent rewrote it: %v then %v", first.Updated, second.Updated)
	}
	if !reflect.DeepEqual(first.AllowedTools, second.AllowedTools) {
		t.Errorf("two reads disagreed on the tool list: %v then %v", first.AllowedTools, second.AllowedTools)
	}
}
