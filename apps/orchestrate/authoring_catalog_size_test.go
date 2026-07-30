package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
	out, err := handler(map[string]any{"names": []any{"create_agent"}})
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
	if _, err := handler(map[string]any{"names": []any{"update_agent", "tool_def"}}); err != nil {
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
