package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// panelFindingFixture gives a user with two agents to resolve voices against.
func panelFindingFixture(t *testing.T) (Database, string) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:u", AuthUser{Username: "u"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	for _, name := range []string{"Skeptic", "Analyst"} {
		if _, err := saveAgent(udb, AgentRecord{ID: "a-" + name, Name: name, Owner: "u",
			OrchestratorPrompt: "You are " + name + "."}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	return udb, "u"
}

// A stage's tool list is the same exact-name filter a machine step's is —
// literally the same function — and the machine editor has caught a typo in
// one since the class was first seen. A pipeline had no such check, so the
// identical mistake failed identically and in silence, hours later, on a turn
// nobody was watching.
func TestAStageNamingAToolNobodyHasIsReportedAtSaveTime(t *testing.T) {
	// A catalog as a caller would hold it, written out by hand: what the
	// test binary happens to have registered is not the subject here.
	known := map[string]bool{"web_search": true, "fetch_url": true, "search_prod_logs": true}

	def := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "look", Tools: []string{"web_search", "Web_Search"}},
	}}
	found := strings.Join(stageToolFindings(def, known), "\n")
	if !strings.Contains(found, "Web_Search") {
		t.Fatalf("the misnamed tool should be reported: %q", found)
	}
	// The recoverable class is mechanical, not imaginative: the case the
	// author had, the separator they used, the raw remote name before a
	// server prefixed it. A guessed spelling costs more than no guess.
	if !strings.Contains(found, `did you mean "web_search"?`) {
		t.Errorf("a case mismatch should carry its correction: %q", found)
	}
	if strings.Contains(found, "\"web_search\" is not") {
		t.Errorf("the correct name should not be flagged: %q", found)
	}

	// A typo one level down is still a typo: loop and fanout bodies are
	// checked like any other stage.
	nested := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "each", Kind: StageLoop, Count: 2, Body: []PipelineStage{
			{Name: "inner", Kind: StageWorker, Prompt: "look", Tools: []string{"Web_Search"}},
		}},
	}}
	if got := strings.Join(stageToolFindings(nested, known), "\n"); !strings.Contains(got, "stage inner") {
		t.Errorf("a nested stage's names must be checked too: %q", got)
	}

	// The whole list missing is its own finding, because the consequence
	// differs in kind: the stage runs tool-less rather than narrowed.
	allBad := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "look", Tools: []string{"nope_one", "nope_two"}},
	}}
	if got := strings.Join(stageToolFindings(allBad, known), "\n"); !strings.Contains(got, "runs with NO tools") {
		t.Errorf("a total miss should say what the stage actually gets: %q", got)
	}

	// The explicit nothing is a setting, not a name, and must never be
	// reported as one.
	marker := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "write", Kind: StageSynthesize, Prompt: "write it", Tools: []string{NoToolsMarker}},
	}}
	if got := stageToolFindings(marker, known); len(got) != 0 {
		t.Errorf("the no-tools marker was reported as an unresolvable tool: %v", got)
	}

	// And a pipeline naming nothing costs nothing — no agent walk, no catalog.
	quiet := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "write", Kind: StageWorker, Prompt: "write it"},
	}}
	if got := stageToolFindings(quiet, known); got != nil {
		t.Errorf("a pipeline that names no tools has nothing to check: %v", got)
	}
}

// A voice that matches no agent is a ROLE — a real option, and the reason a
// panel of perspectives works without authoring three agents first. So it is
// never an error. But resolution is case-insensitive, so the names that fall
// through to roles are genuine misspellings, and "Skpetic" quietly becomes a
// worker impersonating your Skeptic agent: same transcript, different
// thinking, nothing said.
func TestAPanelThatMixesAgentsAndRolesSaysSo(t *testing.T) {
	udb, user := panelFindingFixture(t)

	mixed := PipelineDef{Name: "review", Owner: user, Stages: []PipelineStage{
		{Name: "debate", Kind: StagePanel, Prompt: "argue", Panel: []string{"Skeptic", "Skpetic"}},
	}}
	got := strings.Join(panelVoiceFindings(udb, user, mixed), "\n")
	if !strings.Contains(got, "Skpetic") || !strings.Contains(got, "stage debate") {
		t.Fatalf("a mix should be reported, naming which side each voice fell on: %q", got)
	}
	if !strings.Contains(got, "real option") {
		t.Errorf("it must not read as an error — a role is legitimate: %q", got)
	}

	// All roles is a deliberate panel of perspectives. Silence.
	roles := PipelineDef{Name: "review", Owner: user, Stages: []PipelineStage{
		{Name: "debate", Kind: StagePanel, Prompt: "argue", Panel: []string{"Optimist", "Pessimist", "Cost"}},
	}}
	if got := panelVoiceFindings(udb, user, roles); len(got) != 0 {
		t.Errorf("an all-role panel is normal and should say nothing: %v", got)
	}

	// All agents is fine too.
	agents := PipelineDef{Name: "review", Owner: user, Stages: []PipelineStage{
		{Name: "debate", Kind: StagePanel, Prompt: "argue", Panel: []string{"Skeptic", "Analyst"}},
	}}
	if got := panelVoiceFindings(udb, user, agents); len(got) != 0 {
		t.Errorf("an all-agent panel is normal and should say nothing: %v", got)
	}

	// Case is not a miss: resolution is case-insensitive, so reporting one
	// would be crying wolf about a name that works.
	cased := PipelineDef{Name: "review", Owner: user, Stages: []PipelineStage{
		{Name: "debate", Kind: StagePanel, Prompt: "argue", Panel: []string{"skeptic", "ANALYST"}},
	}}
	if got := panelVoiceFindings(udb, user, cased); len(got) != 0 {
		t.Errorf("case drift resolves and must not be flagged: %v", got)
	}
}

// The reach advice reads the same on both surfaces, because it is the same
// judgement — an author who learned it on a machine step has learned it.
func TestStagesGetTheSameReachAdviceStepsDo(t *testing.T) {
	_, user := panelFindingFixture(t)
	withBundleSource(t)

	def := PipelineDef{Name: "diag", Owner: user, Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "look", Tools: []string{"search_support_bundles"}},
	}}
	got := strings.Join(stageReachAdvice(user, def), "\n")
	if !strings.Contains(got, "stage gather") || !strings.Contains(got, "reach") {
		t.Errorf("a stage naming attachment-minted tools should be nudged: %q", got)
	}

	// And a stage that already declares one has made the choice.
	settled := def
	settled.Stages = []PipelineStage{{Name: "gather", Kind: StageWorker, Prompt: "look",
		Reach: ReachRead, Tools: []string{"search_support_bundles"}}}
	if got := stageReachAdvice(user, settled); len(got) != 0 {
		t.Errorf("a stage with a reach set needs no advice about reaches: %v", got)
	}
}
