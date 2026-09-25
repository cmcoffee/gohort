package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The authoring catalog is the single largest thing in a Builder-capable agent's
// prompt, and it is paid on EVERY turn — including the ones where the agent says
// "Hey Craig". Measured at ~18.7k tokens against a 55.1k prompt: 34% of every
// request, and roughly 3 seconds of a cold prefill on this stack.
//
// This is a budget, not a description. It exists so the next tool added here has
// to be worth its place, rather than the catalog growing a thousand tokens at a
// time until someone measures it again by accident.
func TestAuthoringCatalogStaysWithinBudget(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	app := &OrchestrateApp{}
	app.DB = root
	turn := &chatTurn{app: app, user: "u", udb: udb, agent: AgentRecord{ID: "a1", Name: "Wren", Owner: "u"}}
	sess := &ToolSession{DB: udb}

	measure := func(label string, tools []AgentToolDef) int {
		total := 0
		for _, td := range tools {
			b, _ := json.Marshal(struct {
				N string               `json:"name"`
				D string               `json:"description"`
				P map[string]ToolParam `json:"parameters,omitempty"`
			}{td.Tool.Name, td.Tool.Description, td.Tool.Parameters})
			total += len(b)
		}
		t.Logf("%-28s %3d tools %8d bytes  ~%6d tokens", label, len(tools), total, total/4)
		return total
	}

	auth := measure("builderAuthoringTools", builderAuthoringTools(sess, turn))
	t.Logf("---- authoring catalog alone: ~%d tokens of every single turn ----", auth/4)

	// Deliberately loose: this is a ratchet against unnoticed growth, not a target.
	// If a genuinely necessary tool pushes past it, raise it in the same commit
	// that adds the tool — the point is that the cost gets stated out loud.
	const budgetBytes = 90000
	if auth > budgetBytes {
		t.Errorf("the authoring catalog is %d bytes (~%d tokens), past the %d-byte budget.\n"+
			"Every Builder-capable agent pays this on every turn. Either trim a description, "+
			"move the tool behind lazy loading, or raise the budget here and say why.",
			auth, auth/4, budgetBytes)
	}

	// Biggest individual offenders.
	type row struct {
		n string
		b int
	}
	var rows []row
	for _, td := range builderAuthoringTools(sess, turn) {
		b, _ := json.Marshal(struct {
			D string               `json:"description"`
			P map[string]ToolParam `json:"parameters,omitempty"`
		}{td.Tool.Description, td.Tool.Parameters})
		rows = append(rows, row{td.Tool.Name, len(b)})
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].b > rows[i].b {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	for i, r := range rows {
		if i >= 12 {
			break
		}
		t.Logf("  %2d. %-26s %7d bytes  ~%5d tokens", i+1, r.n, r.b, r.b/4)
	}
}

// The deferral, tested against the turn shape the REAL path produces — which is
// the specific thing v0.5.692's test faked and paid for. resolveWorkerTools runs
// BEFORE setupCustomTools, so at registration time none of the turn's tool maps
// exist yet. The first attempt borrowed lazyCustomToolDefs and panicked on the
// nil write in production while its test, which pre-made the maps, passed.
func newAuthoringTestTurn(t *testing.T) (*chatTurn, *ToolSession) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	app := &OrchestrateApp{}
	app.DB = root
	// Zero-value maps, deliberately: this is the state resolveWorkerTools sees.
	turn := &chatTurn{app: app, user: "u", udb: udb,
		agent: AgentRecord{ID: "a1", Name: "WiWee", Owner: "u", Author: true}}
	return turn, &ToolSession{DB: udb}
}

func TestDeferralSurvivesTheRealInitialisationOrder(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	tools := builderAuthoringTools(sess, turn)

	// Registration on a turn with no maps — the production panic, as a test.
	index := registerLazyAuthoringTools(turn, tools)
	if index == "" {
		t.Fatal("a non-empty catalog must produce an index")
	}

	// setupCustomTools runs AFTER registration on the real path and rebuilds its
	// own maps. The deferred catalog must not live in those maps, or this step
	// silently wipes it — the second defect the borrowed-map version had.
	turn.setupCustomTools(sess)
	if len(turn.deferredAuthoringDefs) != len(tools) {
		t.Fatalf("setupCustomTools wiped the deferred catalog: %d of %d left",
			len(turn.deferredAuthoringDefs), len(tools))
	}

	// Load one through the REAL load_tool handler, not a shortcut.
	handler := turn.loadToolToolDef(sess).Handler
	out, err := handler(context.Background(), map[string]any{"names": []any{"create_agent"}})
	if err != nil {
		t.Fatalf("load_tool: %v", err)
	}
	if !strings.Contains(out, "create_agent") || !strings.Contains(out, "parameters") {
		t.Fatalf("load_tool must return the schema; got: %.200s", out)
	}

	// And it must be CALLABLE afterwards: surfaced by the same dynamic feed the
	// loop polls each round. The reverted version failed here by design — loaded
	// authoring tools never reached the catalog because the feed only reads the
	// session pool.
	surfaced := turn.dynamicNewTempTools(sess)()
	found := false
	for _, td := range surfaced {
		if td.Tool.Name == "create_agent" {
			found = true
			if !td.Tool.RenderLate {
				t.Error("a loaded authoring tool must render late, or loading it re-prefills the whole prompt")
			}
		}
	}
	if !found {
		t.Fatal("loaded but not surfaced — the tool is announced, loadable, and still not callable")
	}

	// Unloaded tools stay out of the catalog; that is the entire saving.
	for _, td := range surfaced {
		if td.Tool.Name == "update_agent" {
			t.Fatal("an unloaded deferred tool must not be in the catalog")
		}
	}

	// Deterministic surfacing order: the tool list is part of the serialized
	// request, and order jitter between rounds busts the prompt cache.
	if _, err := handler(context.Background(), map[string]any{"names": []any{"update_agent", "tool_def"}}); err != nil {
		t.Fatalf("second load: %v", err)
	}
	first := fmt.Sprint(namesOf(turn.loadedDeferredAuthoringTools()))
	for i := 0; i < 5; i++ {
		if got := fmt.Sprint(namesOf(turn.loadedDeferredAuthoringTools())); got != first {
			t.Fatalf("surfacing order must be stable: %s vs %s", first, got)
		}
	}
}

func namesOf(tds []AgentToolDef) []string {
	out := make([]string, 0, len(tds))
	for _, td := range tds {
		out = append(out, td.Tool.Name)
	}
	return out
}

// The index must be a small fraction of the catalog, and every deferred tool must
// remain reachable — deferring a tool must never mean losing it.
func TestDeferredIndexIsAFractionAndLosesNothing(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	tools := builderAuthoringTools(sess, turn)
	full := 0
	for _, td := range tools {
		b, _ := json.Marshal(struct {
			N string               `json:"name"`
			D string               `json:"description"`
			P map[string]ToolParam `json:"parameters,omitempty"`
		}{td.Tool.Name, td.Tool.Description, td.Tool.Parameters})
		full += len(b)
	}
	index := registerLazyAuthoringTools(turn, tools)
	t.Logf("catalog %d bytes (~%d tok) -> index %d bytes (~%d tok), saving ~%d tokens per turn",
		full, full/4, len(index), len(index)/4, (full-len(index))/4)
	if len(index) >= full/4 {
		t.Errorf("index (%d bytes) must be a small fraction of the catalog (%d bytes)", len(index), full)
	}
	for _, td := range tools {
		if _, ok := turn.deferredAuthoringDefs[td.Tool.Name]; !ok {
			t.Errorf("%s deferred but not stored — load_tool cannot return its schema", td.Tool.Name)
		}
		if !strings.Contains(index, "`"+td.Tool.Name+"`") {
			t.Errorf("%s missing from the index — the model cannot know it exists", td.Tool.Name)
		}
	}
}

// A model acting on the index will often skip load_tool and call the tool it
// read about directly. The fallback resolver must treat that exactly as it
// treats a direct call to a lazy custom tool: resolve it, mark it loaded so the
// schema surfaces on later rounds, and never convert a working authoring turn
// into an unknown-tool error.
func TestDeferredToolCalledDirectlyStillResolves(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	registerLazyAuthoringTools(turn, builderAuthoringTools(sess, turn))

	handler, ok := turn.lazyToolFallback("tool_def")
	if !ok || handler == nil {
		t.Fatal("a direct call to a deferred authoring tool must resolve through the fallback")
	}
	// And having been called, it is now loaded: the schema joins the catalog
	// render-late from the next round.
	found := false
	for _, td := range turn.loadedDeferredAuthoringTools() {
		if td.Tool.Name == "tool_def" {
			found = true
		}
	}
	if !found {
		t.Fatal("a directly-called deferred tool must surface as loaded afterwards")
	}
	// Unknown names still miss.
	if _, ok := turn.lazyToolFallback("no_such_tool"); ok {
		t.Fatal("the fallback must not invent tools")
	}
}

// The Builder-rhythm tools (app_def, pipeline, machine, the build-plan card) are
// mounted by catalogKnowTools, not builderAuthoringTools, so deferring the
// catalog used to leave them direct on every Author agent — app_def alone was
// the largest schema in the prompt. Once the turn has deferred, they must join
// the index and stay reachable, and a direct tool the index already lists must
// not be mounted twice.
func TestBuilderRhythmToolsJoinTheDeferredIndex(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	turn.authoringLazyPrompt = registerLazyAuthoringTools(turn, builderAuthoringTools(sess, turn))
	if _, ok := turn.deferredAuthoringDefs["tool_def"]; !ok {
		t.Fatal("precondition: tool_def is part of the deferred authoring catalog")
	}
	direct := []AgentToolDef{
		turn.appDefToolDef(),
		turn.pipelineGroupedToolDef(),
		turn.machineGroupedToolDef(),
		turn.presentBuildPlanToolDef(),
		turn.markStepDoneToolDef(),
		turn.showLinkToolDef(),
		{Tool: Tool{Name: "tool_def", Description: "the self-serve mount"}},
	}
	kept := turn.deferKnownAuthoringTools(direct)
	if got := namesOf(kept); len(got) != 1 || got[0] != "show_link" {
		t.Fatalf("only show_link should stay direct, got %v", got)
	}
	for _, n := range []string{"app_def", "pipeline", "machine", "present_build_plan", "mark_step_done"} {
		if _, ok := turn.deferredAuthoringDefs[n]; !ok {
			t.Errorf("%s deferred but not stored — load_tool cannot return its schema", n)
		}
		if !strings.Contains(turn.authoringLazyPrompt, "`"+n+"`") {
			t.Errorf("%s missing from the index — the model cannot know it exists", n)
		}
		if h, ok := turn.lazyToolFallback(n); !ok || h == nil {
			t.Errorf("%s must still resolve when called directly", n)
		}
	}
	if strings.Count(turn.authoringLazyPrompt, "- `tool_def`") != 1 {
		t.Error("tool_def must be listed in the index exactly once")
	}
}

// A turn that did not defer its catalog — Builder, or an author running for a
// non-owner — must get its tools back untouched.
func TestNoDeferralLeavesDirectToolsAlone(t *testing.T) {
	turn, _ := newAuthoringTestTurn(t)
	direct := []AgentToolDef{turn.appDefToolDef(), turn.pipelineGroupedToolDef()}
	if got := turn.deferKnownAuthoringTools(direct); len(got) != 2 {
		t.Fatalf("no deferral this turn, tools must pass through, got %v", namesOf(got))
	}
	if turn.authoringLazyPrompt != "" {
		t.Fatal("no index may appear when nothing was deferred")
	}
}

// tool_def is Builder's alone: tools stopped being self-serve in v0.7.146, and
// a tool is built by Builder like everything else. So the tool is constructed in
// exactly one place, Builder's authoring catalog; a second mount would hand it
// back to every agent with no test noticing.
func TestOnlyBuildersCatalogMountsToolDef(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Skip("package sources unavailable")
	}
	var at []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for i, l := range strings.Split(string(data), "\n") {
			if strings.Contains(l, "temptool.BuildToolDef()") && !strings.HasPrefix(strings.TrimSpace(l), "//") {
				at = append(at, fmt.Sprintf("%s:%d", f, i+1))
			}
		}
	}
	if len(at) != 1 || !strings.HasPrefix(at[0], "builder_tools.go:") {
		t.Errorf("tool_def should be built only in builderAuthoringTools, found at %v", at)
	}
}

// Builder authors constantly and keeps everything inline.
func TestBuilderKeepsToolsDirect(t *testing.T) {
	turn, _ := newAuthoringTestTurn(t)
	turn.agent.ID = "seed-builder"
	direct := []AgentToolDef{turn.appDefToolDef(), {Tool: Tool{Name: "tool_def"}}}
	if got := turn.deferKnownAuthoringTools(direct); len(got) != 2 || turn.authoringLazyPrompt != "" {
		t.Fatalf("Builder's tools must pass through untouched, got %v", namesOf(got))
	}
}

// recurring is not authoring, so it must land under its own index section on
// any agent, Builder included, and stay reachable. When authoring was deferred
// too, both sections coexist and neither loses its tools.
func TestRecurringDeferredUnderItsOwnSection(t *testing.T) {
	for _, id := range []string{"a1", "seed-builder"} {
		turn, _ := newAuthoringTestTurn(t)
		turn.agent.ID = id
		kept := turn.deferOnDemandTools([]AgentToolDef{turn.showLinkToolDef(), turn.recurringToolDef()})
		if got := namesOf(kept); len(got) != 1 || got[0] != "show_link" {
			t.Fatalf("%s: only show_link should stay direct, got %v", id, got)
		}
		if !strings.Contains(turn.authoringLazyPrompt, onDemandToolIndexHeader) || !strings.Contains(turn.authoringLazyPrompt, "- `recurring`") {
			t.Fatalf("%s: recurring must be indexed under its own section, got %q", id, turn.authoringLazyPrompt)
		}
		if strings.Contains(turn.authoringLazyPrompt, "Authoring tools") {
			t.Fatalf("%s: a scheduler must not be presented as an authoring tool", id)
		}
		if h, ok := turn.lazyToolFallback("recurring"); !ok || h == nil {
			t.Fatalf("%s: recurring must still resolve when called directly", id)
		}
	}

	turn, sess := newAuthoringTestTurn(t)
	turn.authoringLazyPrompt = registerLazyAuthoringTools(turn, builderAuthoringTools(sess, turn))
	turn.deferOnDemandTools([]AgentToolDef{turn.recurringToolDef()})
	if _, ok := turn.deferredAuthoringDefs["tool_def"]; !ok {
		t.Fatal("deferring recurring must not wipe the authoring catalog")
	}
	if _, ok := turn.deferredAuthoringDefs["recurring"]; !ok {
		t.Fatal("recurring must join the existing deferred set")
	}
}

// The entity-graph trio rides the same on-demand section as recurring: off the
// direct catalog, listed in the index, and still callable without a load.
func TestGraphToolsDeferredOnDemand(t *testing.T) {
	turn, _ := newAuthoringTestTurn(t)
	direct := []AgentToolDef{turn.showLinkToolDef(), turn.linkEntitiesToolDef(), turn.recallAboutToolDef(), turn.forgetGraphToolDef()}
	kept := turn.deferOnDemandTools(direct)
	if got := namesOf(kept); len(got) != 1 || got[0] != "show_link" {
		t.Fatalf("only show_link should stay direct, got %v", got)
	}
	for _, n := range []string{"link_entities", "recall_about", "forget_graph"} {
		if !strings.Contains(turn.authoringLazyPrompt, "- `"+n+"`") {
			t.Errorf("%s missing from the on-demand index", n)
		}
		if h, ok := turn.lazyToolFallback(n); !ok || h == nil {
			t.Errorf("%s must still resolve when called directly", n)
		}
	}
	if strings.Count(turn.authoringLazyPrompt, onDemandToolIndexHeader) != 1 {
		t.Error("the on-demand section header must appear exactly once")
	}
}

// load_tool on a tool that is already in the catalog must say so and stop. It
// used to fall through to the persistent pool and, when an entry shared the
// name, append it as a second definition (a live fetch_url_ts3_api collision).
// A deferred tool is not mounted and must still load.
func TestLoadToolOnMountedToolSaysAlreadyLoaded(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	turn.authoringLazyPrompt = registerLazyAuthoringTools(turn, builderAuthoringTools(sess, turn))
	turn.noteMountedTools([]AgentToolDef{{Tool: Tool{Name: "fetch_url_ts3_api"}}, turn.showLinkToolDef()})

	out, err := turn.loadToolToolDef(sess).Handler(context.Background(), map[string]any{
		"names": []any{"fetch_url_ts3_api", "tool_def", "no_such_tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Already loaded (call directly): fetch_url_ts3_api") {
		t.Errorf("a mounted tool must be reported as already loaded, got %q", out)
	}
	if turn.loadedCustomTools["fetch_url_ts3_api"] {
		t.Error("a mounted tool must not be re-loaded as a custom tool")
	}
	if !strings.Contains(out, "Loaded tool_def") {
		t.Errorf("a deferred tool is not mounted and must still load, got %q", out)
	}
	if !strings.Contains(out, "Unknown") || !strings.Contains(out, "no_such_tool") {
		t.Errorf("an unknown name must still be reported, got %q", out)
	}
}

// An index line is a short lead, never a paragraph: first sentence, at most
// indexLeadMax runes, cut on a word, never through a multi-byte character.
func TestIndexLeadIsShortAndClean(t *testing.T) {
	cases := map[string]string{
		"Short one.":                            "Short one.",
		"First sentence. Second sentence here.": "First sentence.",
		"Spans\nlines   and  spaces.":           "Spans lines and spaces.",
		"Use a template (e.g. github). More.":   "Use a template (e.g. github).",
	}
	for in, want := range cases {
		if got := indexLead(in, indexLeadMax); got != want {
			t.Errorf("indexLead(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("wordy ", 30) + "é"
	got := indexLead(long, indexLeadMax)
	if n := len([]rune(got)); n > indexLeadMax+1 || !strings.HasSuffix(got, "…") || strings.HasSuffix(got, " …") {
		t.Errorf("long lead not cut cleanly: %q (%d runes)", got, n)
	}
	if !utf8.ValidString(indexLead(strings.Repeat("é", 200), indexLeadMax)) {
		t.Error("cut split a multi-byte character")
	}
}

// Print the real index so the leads can be read, not just counted.
func TestAuthoringIndexReadable(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	turn.authoringLazyPrompt = registerLazyAuthoringTools(turn, builderAuthoringTools(sess, turn))
	turn.deferKnownAuthoringTools([]AgentToolDef{turn.appDefToolDef(), turn.pipelineGroupedToolDef(), turn.machineGroupedToolDef()})
	t.Logf("index %d bytes:\n%s", len(turn.authoringLazyPrompt), turn.authoringLazyPrompt)
}
