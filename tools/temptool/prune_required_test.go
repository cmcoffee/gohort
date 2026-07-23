package temptool

import "testing"

// Removing a param from an action must drop its required entry too. Leaving a
// stale required strands a name the schema no longer declares, and dispatch
// then demands a param that help doesn't list — the "requires param
// phone_number_id" loop that could not be fixed from the model's side.
func TestPruneRequired(t *testing.T) {
	params := map[string]any{
		"customer_number": map[string]any{"type": "string"},
		"goal":            map[string]any{"type": "string"},
	}
	got := pruneRequired([]any{"customer_number", "phone_number_id", "goal"}, params)
	list, ok := got.([]any)
	if !ok {
		t.Fatalf("expected a list, got %T", got)
	}
	if len(list) != 2 || list[0] != "customer_number" || list[1] != "goal" {
		t.Errorf("stale required not pruned: %v", list)
	}
}

func TestPruneRequiredLeavesUnknownShapes(t *testing.T) {
	// Non-list required, non-map params, nil — all returned untouched rather
	// than discarded. This runs in an edit path; losing data we merely failed
	// to parse would be worse than the bug.
	if got := pruneRequired("weird", map[string]any{}); got != "weird" {
		t.Errorf("non-list required should pass through: %v", got)
	}
	if got := pruneRequired([]any{"x"}, "not-a-map"); len(got.([]any)) != 1 {
		t.Errorf("non-map params should leave required alone")
	}
	if got := pruneRequired(nil, map[string]any{}); got != nil {
		t.Errorf("nil required should pass through")
	}
	// A non-string entry (unexpected shape) is kept, not dropped.
	got := pruneRequired([]any{42, "customer_number"}, map[string]any{"customer_number": map[string]any{}})
	if len(got.([]any)) != 2 {
		t.Errorf("unrecognized entry should be kept: %v", got)
	}
}
