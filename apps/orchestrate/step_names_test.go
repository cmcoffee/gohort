package orchestrate

import (
	"strings"
	"testing"
)

// A step whose tool is sent as an object is refused, saying the name is a
// string and args and next sit beside it; it was saved calling
// "map[args:... tool:geo]" with its args and next never read.
func TestAStepNamesItsToolWithAString(t *testing.T) {
	phase := map[string]any{"name": "geocode", "tool": map[string]any{"tool": "geo", "args": map[string]any{"q": "{input}"}, "next": "forecast"}}
	_, err := parseMachinePhases([]any{phase})
	if err == nil || !strings.Contains(err.Error(), `"tool" is the tool's NAME as a string`) || !strings.Contains(err.Error(), "phase 1 (geocode)") {
		t.Fatalf("machine phase: %v", err)
	}
	stage := map[string]any{"name": "s", "kind": "tool", "tool": map[string]any{"tool": "geo"}}
	if _, err := parsePipelineStages([]any{stage}); err == nil || !strings.Contains(err.Error(), "NAME as a string") {
		t.Fatalf("pipeline stage: %v", err)
	}
	if why := nonStringNames(map[string]any{"agent": 7}); !strings.Contains(why, `"agent" is a NAME`) {
		t.Fatalf("agent: %q", why)
	}
	if why := nonStringNames(map[string]any{"tool": "geo", "agent": "helper"}); why != "" {
		t.Fatalf("string names refused: %q", why)
	}
}

// Leaving out which app or machine says so, rather than "not found", which
// sent the builder checking a name that was right.
func TestNamingNothingSaysSo(t *testing.T) {
	if err := appNotFound(map[string]any{"script": "totals"}, ""); !strings.HasPrefix(err.Error(), "id is required") {
		t.Fatalf("app, none named: %v", err)
	}
	if err := appNotFound(map[string]any{"id": "nope"}, "to verify"); !strings.Contains(err.Error(), "no matching app to verify") {
		t.Fatalf("app, named: %v", err)
	}
	if err := machineNotFound(map[string]any{"phase": "track"}); !strings.HasPrefix(err.Error(), "name (or id) is required") {
		t.Fatalf("machine, none named: %v", err)
	}
	if err := machineNotFound(map[string]any{"name": "nope"}); !strings.HasPrefix(err.Error(), "no machine found") {
		t.Fatalf("machine, named: %v", err)
	}
}
