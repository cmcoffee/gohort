package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestReferenceSelectionsFromArgs — models produce a flat string list far more
// reliably than an array of objects, so the CRUD tools take "<kind>:<item_id>".
// The split is on the FIRST colon: a kind never contains one, an id might.
func TestReferenceSelectionsFromArgs(t *testing.T) {
	got := referenceSelectionsFromArgs(map[string]any{
		"attached_sources": []any{
			"system:abc-123",
			" system : def-456 ",   // whitespace around both halves
			"system:abc-123",       // duplicate
			"confluence:space:ENG", // id containing a colon
			"nocolon",              // unusable
			"system:",              // no id
			":abc",                 // no kind
			"",                     // empty
		},
	}, "attached_sources")

	want := []struct{ kind, item string }{
		{"system", "abc-123"},
		{"system", "def-456"},
		{"confluence", "space:ENG"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d selections, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].ItemID != w.item {
			t.Errorf("selection %d = %+v, want %s/%s", i, got[i], w.kind, w.item)
		}
	}
}

// TestReferenceSelectionsRejectsRatherThanGuesses — a value with no kind must be
// dropped, never defaulted. Inventing one attaches the agent to a source nobody
// chose, and the agent then answers from it as if the owner had asked for that.
func TestReferenceSelectionsRejectsRatherThanGuesses(t *testing.T) {
	got := referenceSelectionsFromArgs(map[string]any{
		"attached_sources": []any{"some-appliance-id", "another"},
	}, "attached_sources")
	if len(got) != 0 {
		t.Errorf("bare ids were accepted as selections: %+v", got)
	}
	if referenceSelectionsFromArgs(map[string]any{}, "attached_sources") != nil {
		t.Error("a missing parameter should parse to nil")
	}
}

// TestRenderReferenceSourcesIsEmptyHanded — with nothing registered the tool has
// to say so, not return a blank that reads as "there are none of these anywhere".
func TestRenderReferenceSourcesEmpty(t *testing.T) {
	out := renderReferenceSources("nobody-with-this-name")
	if strings.TrimSpace(out) == "" {
		t.Fatal("empty listing returned nothing at all")
	}
	if !strings.Contains(out, "No reference sources") {
		t.Errorf("empty listing = %q", out)
	}
}

// TestListReferenceSourcesToolShape — the tool the Builder uses to discover what
// attached_sources will accept. Its name is referenced by the create_agent /
// update_agent parameter docs, so a rename there silently strands that guidance.
func TestListReferenceSourcesToolShape(t *testing.T) {
	td := listReferenceSourcesToolDef("someone")
	if td.Tool.Name != "list_reference_sources" {
		t.Errorf("tool name = %q; create_agent's attached_sources docs point at list_reference_sources", td.Tool.Name)
	}
	if len(td.Tool.Parameters) != 0 {
		t.Errorf("tool takes %d parameters, want none", len(td.Tool.Parameters))
	}
	out, err := td.Handler(map[string]any{})
	if err != nil {
		t.Fatalf("handler errored: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("handler returned nothing")
	}
}

// TestAttachedSourceToolsDedupeByName — two attachments can mint the same tool
// name (the same system attached twice, or two names that slug identically).
// First wins; a catalog with a duplicate name has undefined dispatch.
func TestAttachedSourceToolsDedupeByName(t *testing.T) {
	turn := &chatTurn{user: "u", agent: AgentRecord{
		ID: "a1",
		AttachedSources: []ReferenceSelection{
			{Kind: "system", ItemID: "x"},
			{Kind: "system", ItemID: "x"},
		},
	}}
	defs := turn.buildAttachedSourceToolDefs()
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Tool.Name] {
			t.Errorf("duplicate tool name %q in the agent's catalog", d.Tool.Name)
		}
		seen[d.Tool.Name] = true
	}
}

// TestNoAttachedSourcesIsNotAnError — an agent with none must build no tools and
// no complaint, since that is every agent that has not opted in.
func TestNoAttachedSourcesIsNotAnError(t *testing.T) {
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "a1"}}
	if defs := turn.buildAttachedSourceToolDefs(); len(defs) != 0 {
		t.Errorf("an agent with no attached sources built %d tools", len(defs))
	}
}

// TestBuilderAuthoringToolsSurvivesANilTurn — t is documented optional and
// three dispatch paths pass nil (agent_dispatch x2, agents_grouped_tool). A
// tool added here that dereferences it panics every delegated Builder run,
// which is what happened: a nil-turn crash reached production from a channel
// message, not from anything a test exercised.
func TestBuilderAuthoringToolsSurvivesANilTurn(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("builderAuthoringTools panicked with a nil turn: %v", r)
		}
	}()
	tools := builderAuthoringTools(&ToolSession{Username: "craig"}, nil)
	if len(tools) == 0 {
		t.Error("no authoring tools built")
	}
	found := false
	for _, td := range tools {
		if td.Tool.Name == "list_reference_sources" {
			found = true
		}
	}
	if !found {
		t.Error("list_reference_sources missing — it should still build without a turn")
	}
}

// TestBuilderAuthoringToolsSurvivesNoSessionEither — belt and braces: neither
// carrier present should degrade to an empty user, not a crash.
func TestBuilderAuthoringToolsSurvivesNoSessionEither(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("builderAuthoringTools panicked with nil session and turn: %v", r)
		}
	}()
	_ = builderAuthoringTools(nil, nil)
}
