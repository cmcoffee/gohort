package orchestrate

import (
	"reflect"
	"strings"
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
	shadow.OverriddenFields = agentOverrides(seed, shadow, frameworkOwnedSeedFields(seed.ID))
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

// --- instances -------------------------------------------------------------

// Cloning a shape produces an agent that TRACKS it. Until this, the wizard and
// materializeArchetypeAgent handed out snapshots, so a fix to the research
// prompt reached nobody who already had a research agent.
func TestACloneOfAShapeTracksIt(t *testing.T) {
	db := overlayTestDB(t)
	clone, err := cloneAgent(db, "seed-research", "craig@example.com", "My Researcher", true)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ShapeID != "research" {
		t.Fatalf("the clone tracks %q, want the research shape", clone.ShapeID)
	}
	// The name is the owner's, always: renaming somebody's agent because the
	// framework renamed a shape is jarring and has no upside.
	if !hasField(clone.OverriddenFields, "name") {
		t.Errorf("the clone's name is not its own: %v", clone.OverriddenFields)
	}
	if clone.Name != "My Researcher" {
		t.Errorf("name = %q", clone.Name)
	}
	if clone.Owner != "craig@example.com" || clone.ID == "seed-research" {
		t.Errorf("the clone is not the user's own record: owner=%q id=%q", clone.Owner, clone.ID)
	}
}

// A shape improvement reaches an agent created before it, in every field its
// owner never claimed.
func TestAShapeImprovementReachesAnExistingInstance(t *testing.T) {
	db := overlayTestDB(t)
	clone, err := cloneAgent(db, "seed-research", "craig@example.com", "My Researcher", true)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	// The owner tightens one budget, months later.
	clone.MaxWorkerRounds = 3
	if _, err := saveAgent(db, clone); err != nil {
		t.Fatalf("save: %v", err)
	}

	var stored AgentRecord
	if !db.Get(agentsTable, clone.ID, &stored) {
		t.Fatal("no stored instance")
	}
	// Simulate the framework moving the shape by resolving against a changed
	// base: the same thing a new release does.
	base, ok := shapeBaseRecord("research")
	if !ok {
		t.Fatal("the research shape does not resolve")
	}
	next := base
	next.PlanGuidance = "Decompose differently."
	next.MaxWorkerRounds = 40
	got := applyAgentOverrides(next, stored, stored.OverriddenFields, nil)

	if got.PlanGuidance != next.PlanGuidance {
		t.Error("a shape improvement did not reach an agent that never claimed that field")
	}
	if got.MaxWorkerRounds != 3 {
		t.Errorf("the owner's own budget was overwritten: %d", got.MaxWorkerRounds)
	}
}

// An instance may claim ANY field, including the persona. That is the
// difference from a seed shadow: a shadow is the framework's agent and may not
// rewrite what it is, while an instance is already the user's own.
func TestAnInstanceMayRewriteItsPersona(t *testing.T) {
	db := overlayTestDB(t)
	clone, err := cloneAgent(db, "seed-research", "craig@example.com", "My Researcher", true)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	clone.OrchestratorPrompt = "You are mine, and you answer only about boats."
	saved, err := saveAgent(db, clone)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !hasField(saved.OverriddenFields, "orchestrator_prompt") {
		t.Fatalf("the rewritten persona was not recorded: %v", saved.OverriddenFields)
	}
	got, ok := loadAgent(db, saved.ID)
	if !ok {
		t.Fatal("not found")
	}
	if got.OrchestratorPrompt != "You are mine, and you answer only about boats." {
		t.Errorf("the owner's persona was replaced by the shape's: %q", got.OrchestratorPrompt)
	}
}

// Cloning a user's own agent tracks nothing: there is no shape behind it, and
// inheriting the source's override list would claim the source's decisions as
// this record's own.
func TestCloningAPlainAgentTracksNothing(t *testing.T) {
	db := overlayTestDB(t)
	first, err := cloneAgent(db, "seed-research", "craig@example.com", "First", true)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	second, err := cloneAgent(db, first.ID, "craig@example.com", "Second", true)
	if err != nil {
		t.Fatalf("clone of a clone: %v", err)
	}
	if second.ShapeID != "" {
		t.Errorf("a copy of a user's agent tracks %q", second.ShapeID)
	}
}

// An instance whose shape is gone keeps working. The stored row is a full
// agent, so losing the link costs nothing but future updates.
func TestAnInstanceSurvivesAMissingShape(t *testing.T) {
	db := overlayTestDB(t)
	clone, err := cloneAgent(db, "seed-research", "craig@example.com", "My Researcher", true)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	var stored AgentRecord
	if !db.Get(agentsTable, clone.ID, &stored) {
		t.Fatal("no stored instance")
	}
	stored.ShapeID = "a_shape_that_was_removed"
	got := resolveShapeInstance(stored)
	if got.Name != "My Researcher" || strings.TrimSpace(got.OrchestratorPrompt) == "" {
		t.Errorf("an instance with a missing shape lost its content: name=%q prompt=%d bytes",
			got.Name, len(got.OrchestratorPrompt))
	}
}

// Only shapes that ship a record can be instantiated. A watcher's subject
// varies, so there is nothing to copy and nothing to track.
func TestOnlyShapesWithARecordInstantiate(t *testing.T) {
	if _, ok := shapeBaseRecord("research"); !ok {
		t.Error("research does not instantiate")
	}
	if _, ok := shapeBaseRecord("scheduled_watcher"); ok {
		t.Error("scheduled_watcher instantiates, but it describes an agent whose subject is not known yet")
	}
	if _, ok := shapeBaseRecord("investigator"); ok {
		t.Error("investigator instantiates, but it describes a sub-agent pointed at a subject")
	}
}

// Detaching freezes the agent as it READS, not as it was stored. The stored
// row's unclaimed fields hold whatever the shape said when the agent was
// created, so writing that back would silently revert the agent to an older
// version of itself at the moment its owner asked to freeze it.
func TestDetachingFreezesWhatTheAgentReadsNow(t *testing.T) {
	db := overlayTestDB(t)
	clone, err := cloneAgent(db, "seed-research", "craig@example.com", "My Researcher", true)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	live, ok := loadAgent(db, clone.ID)
	if !ok {
		t.Fatal("not found")
	}

	detached, err := detachAgentFromShape(db, clone.ID)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if detached.ShapeID != "" || len(detached.OverriddenFields) != 0 || detached.OverlayRev != 0 {
		t.Errorf("still tracking after detach: shape=%q fields=%v rev=%d",
			detached.ShapeID, detached.OverriddenFields, detached.OverlayRev)
	}
	if detached.Name != "My Researcher" {
		t.Errorf("name = %q", detached.Name)
	}
	if detached.OrchestratorPrompt != live.OrchestratorPrompt {
		t.Error("detaching changed the agent's prompt, which is the one thing freezing must not do")
	}
	if detached.MaxWorkerRounds != live.MaxWorkerRounds || detached.MaxPlanSteps != live.MaxPlanSteps {
		t.Errorf("detaching changed a budget: %d/%d, was %d/%d",
			detached.MaxWorkerRounds, detached.MaxPlanSteps, live.MaxWorkerRounds, live.MaxPlanSteps)
	}

	// And it stays frozen: a later shape change reaches nothing.
	var stored AgentRecord
	if !db.Get(agentsTable, clone.ID, &stored) {
		t.Fatal("no stored record")
	}
	base, _ := shapeBaseRecord("research")
	next := base
	next.PlanGuidance = "Decompose differently."
	if got := resolveShapeInstance(stored); got.PlanGuidance == next.PlanGuidance {
		t.Error("a detached agent still followed the shape")
	}

	// Detaching twice is an error worth reading, not a silent no-op.
	if _, err := detachAgentFromShape(db, clone.ID); err == nil {
		t.Error("detaching an agent that follows nothing reported success")
	}
}

// The agent a person talks to every day is the one built by the first-run
// wizard, and it was the one agent following nothing: built from an empty
// struct, so it carried no budgets and fell back to the framework floor, and
// no framework improvement could ever reach it.
func TestTheWizardsAssistantFollowsTheConversationalShape(t *testing.T) {
	shape, ok := shapeBaseRecord(assistantShape)
	if !ok {
		t.Fatalf("the %q shape does not ship a record", assistantShape)
	}

	got := wizardBaseRecord("assistant")
	if got.ShapeID != assistantShape {
		t.Errorf("a wizard assistant follows %q", got.ShapeID)
	}
	if got.ID != "" {
		t.Errorf("it starts as the framework's own record: id=%q", got.ID)
	}
	if got.Hidden || got.Exposed {
		t.Errorf("it inherited a seed's visibility: hidden=%v exposed=%v", got.Hidden, got.Exposed)
	}
	// The settings nobody would think to ask a new user about.
	if got.MaxWorkerRounds != shape.MaxWorkerRounds || got.MaxPlanSteps != shape.MaxPlanSteps {
		t.Errorf("budgets = %d/%d, want the shape's %d/%d",
			got.MaxWorkerRounds, got.MaxPlanSteps, shape.MaxWorkerRounds, shape.MaxPlanSteps)
	}
	if got.MaxWorkerRounds == 0 {
		t.Error("a wizard assistant still starts with no round budget at all")
	}
	if !got.AllowPrivateMode || !got.PreMortem {
		t.Errorf("it lost a shape default: private=%v premortem=%v", got.AllowPrivateMode, got.PreMortem)
	}
	// Not everything is inherited. Conductor tools are a block of prompt on
	// every turn, and the caller turns them on for the FIRST-RUN assistant
	// alone; taking them from the shape would hand them to every later
	// assistant too, and quietly.
	if got.Fleet {
		t.Error("every wizard assistant now carries the conductor toolset, which was meant for the first-run one")
	}

	// A specialist has no shape: what it DOES is the whole question, and only
	// the wizard's brief answers it.
	if s := wizardBaseRecord("specialist"); s.ShapeID != "" {
		t.Errorf("a specialist follows %q", s.ShapeID)
	}
}

// And once saved, the drafted persona is the agent's own while everything
// untouched keeps tracking.
func TestAWizardAssistantKeepsItsPersonaAndTracksTheRest(t *testing.T) {
	db := overlayTestDB(t)
	rec := wizardBaseRecord("assistant")
	rec.Owner = "craig@example.com"
	rec.Name = "Otto"
	rec.Description = "My assistant."
	rec.OrchestratorPrompt = "You are Otto. You are dry, brief, and you never flatter."

	saved, err := saveAgent(db, rec)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, want := range []string{"name", "orchestrator_prompt"} {
		if !hasField(saved.OverriddenFields, want) {
			t.Errorf("%q is not recorded as the agent's own: %v", want, saved.OverriddenFields)
		}
	}
	if hasField(saved.OverriddenFields, "max_worker_rounds") {
		t.Error("the round budget was claimed as the user's, so a framework change will never reach it")
	}

	got, ok := loadAgent(db, saved.ID)
	if !ok {
		t.Fatal("not found")
	}
	if got.OrchestratorPrompt != rec.OrchestratorPrompt {
		t.Error("the drafted persona was replaced by the shape's")
	}
	if got.Name != "Otto" {
		t.Errorf("name = %q", got.Name)
	}
}

// A clone of a framework record is an ordinary agent of the user's, including
// in whether other agents may dispatch it. The shape ships Hidden so the SEED
// stays out of dispatch lists, and the seed's own note says the clones are
// where that decision gets made; inheriting it meant every agent made from a
// template arrived with a posture nobody had chosen.
func TestCloningAShapeDoesNotInheritItsHiddenPosture(t *testing.T) {
	db := overlayTestDB(t)
	seed, ok := seedAgentByID("seed-kb")
	if !ok || !seed.Hidden {
		t.Fatal("seed-kb is not the hidden template this test is about")
	}
	clone, err := cloneAgent(db, "seed-kb", "craig@example.com", "Handbook", false)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.Hidden {
		t.Error("an agent cloned from a shape arrived hidden from every fleet")
	}

	// A copy of the user's OWN agent inherits what they set, because there the
	// value is a decision somebody made on purpose.
	mine, err := saveAgent(db, AgentRecord{
		Owner: "craig@example.com", Name: "Private helper",
		OrchestratorPrompt: "You help.", Hidden: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	copyOfMine, err := cloneAgent(db, mine.ID, "craig@example.com", "Second helper", false)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !copyOfMine.Hidden {
		t.Error("copying a hidden agent of my own published it")
	}
}
