package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The catalog is assembled from several independent appends, and a name can
// legitimately appear in two of them — Builder's seed allowlists stay_silent
// and keep_going, and the framework supplies them too. The agent loop coped by
// keeping the first and logging a collision, but its warning blamed "an
// expanded toolbox action and a standalone tool" — a real cause of collisions,
// and not this one. So every reader was pointed at the wrong thing.
func TestDedupeToolsByNameKeepsFirst(t *testing.T) {
	tools := []AgentToolDef{
		{Tool: Tool{Name: "stay_silent", Description: "first"}},
		{Tool: Tool{Name: "keep_going"}},
		{Tool: Tool{Name: "stay_silent", Description: "second"}},
		{Tool: Tool{Name: "find_tools"}},
		{Tool: Tool{Name: "keep_going"}},
	}
	names := []string{"stay_silent", "keep_going", "stay_silent", "find_tools"}

	gotTools, gotNames := dedupeToolsByName(tools, names)

	if len(gotTools) != 3 {
		t.Fatalf("got %d tools, want 3: %+v", len(gotTools), gotTools)
	}
	// First definition wins — same rule the agent loop applied, so behavior
	// is unchanged and only the duplicates disappear.
	if gotTools[0].Tool.Description != "first" {
		t.Errorf("kept the later definition of stay_silent: %q", gotTools[0].Tool.Description)
	}
	// Order is preserved: the catalog's ordering is meaningful to the model.
	want := []string{"stay_silent", "keep_going", "find_tools"}
	for i, w := range want {
		if gotTools[i].Tool.Name != w {
			t.Errorf("tool %d = %q, want %q", i, gotTools[i].Tool.Name, w)
		}
	}
	if len(gotNames) != 3 {
		t.Errorf("names not deduped: %v", gotNames)
	}
}

// A catalog with no duplicates must come back untouched.
func TestDedupeToolsByNameLeavesCleanCatalogAlone(t *testing.T) {
	tools := []AgentToolDef{
		{Tool: Tool{Name: "a"}}, {Tool: Tool{Name: "b"}}, {Tool: Tool{Name: "c"}},
	}
	got, _ := dedupeToolsByName(tools, []string{"a", "b", "c"})
	if len(got) != 3 {
		t.Fatalf("dropped tools from a clean catalog: %d", len(got))
	}
}

func TestDedupeToolsByNameHandlesEmpty(t *testing.T) {
	got, names := dedupeToolsByName(nil, nil)
	if len(got) != 0 || len(names) != 0 {
		t.Error("empty input should stay empty")
	}
}
