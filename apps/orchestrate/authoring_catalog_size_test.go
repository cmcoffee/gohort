package orchestrate

import (
	"encoding/json"
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

// Deferring the catalog is the fix for the budget above: an agent that CAN author
// but mostly converses should not prefill 19 tool schemas to say "hello". The
// index carries names and one-liners; load_tool fetches the rest.
func TestDeferredAuthoringIndexIsAFractionOfTheCatalog(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	app := &OrchestrateApp{}
	app.DB = root
	turn := &chatTurn{
		app: app, user: "u", udb: udb,
		agent:               AgentRecord{ID: "a1", Name: "Wren", Owner: "u", Author: true},
		lazyCustomToolNames: map[string]bool{},
		lazyCustomToolDefs:  map[string]AgentToolDef{},
	}
	sess := &ToolSession{DB: udb}
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
		t.Errorf("the index (%d bytes) must be a small fraction of the catalog (%d bytes), or deferring buys nothing", len(index), full)
	}
	// Every tool must still be REACHABLE, or deferring becomes losing them.
	for _, td := range tools {
		if !turn.lazyCustomToolNames[td.Tool.Name] {
			t.Errorf("%s was deferred but not registered for load_tool — it is now unreachable", td.Tool.Name)
		}
		if _, ok := turn.lazyCustomToolDefs[td.Tool.Name]; !ok {
			t.Errorf("%s has no stored definition, so load_tool cannot return its schema", td.Tool.Name)
		}
		if !strings.Contains(index, "`"+td.Tool.Name+"`") {
			t.Errorf("%s is missing from the index, so the model has no way to know it exists", td.Tool.Name)
		}
	}
}
