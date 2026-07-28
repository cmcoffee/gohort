package core

import "testing"

// Tier and thinking are independent axes of one route value. They used to be
// tangled: thinking was expressible on WORKER values only, so escalating a
// stage to the lead silently dropped the explicit thinking flag it carried as
// a worker — one toggle, two changes, and the second one invisible.

func TestRouteValueIsLead(t *testing.T) {
	for _, v := range []string{"lead", "lead (thinking)", "", "anything-unknown"} {
		if !RouteValueIsLead(v) {
			t.Errorf("%q should escalate to lead (empty/unknown means stage default)", v)
		}
	}
	for _, v := range []string{"worker", "worker (thinking)"} {
		if RouteValueIsLead(v) {
			t.Errorf("%q must stay on the worker", v)
		}
	}
}

// TestRouteValuesCoverBothAxes — every combination of tier and thinking must be
// expressible, or a stage is forced to trade one for the other.
func TestRouteValuesCoverBothAxes(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range RouteValues() {
		seen[v] = true
	}
	for _, want := range []string{"lead", "lead (thinking)", "worker", "worker (thinking)"} {
		if !seen[want] {
			t.Errorf("route vocabulary is missing %q", want)
		}
	}
	if len(RouteValues()) != 4 {
		t.Errorf("expected exactly 4 values (2 tiers x thinking), got %v", RouteValues())
	}
	// Both admin gates key off this list, so a value that isn't a legal tier
	// would be selectable and then rejected on save.
	for _, v := range RouteValues() {
		lead := RouteValueIsLead(v)
		if lead && v != "lead" && v != "lead (thinking)" {
			t.Errorf("%q classified as lead but isn't a lead value", v)
		}
	}
}

// TestRouteThinkReadsBothTiers pins the mapping the whole fix rests on: a
// "(thinking)" value means thinking WHICHEVER tier it names.
func TestRouteThinkReadsBothTiers(t *testing.T) {
	prev := LookupRouteFunc
	defer func() { LookupRouteFunc = prev }()

	cases := map[string]*bool{
		"worker (thinking)": routeThinkPtr(true),
		"lead (thinking)":   routeThinkPtr(true),
		"worker":            routeThinkPtr(false),
		"lead":              nil, // provider default
		"":                  nil,
	}
	for val, want := range cases {
		LookupRouteFunc = func(string) string { return val }
		got := RouteThink("any.stage")
		switch {
		case want == nil && got != nil:
			t.Errorf("%q: think = %v, want nil (provider default)", val, *got)
		case want != nil && got == nil:
			t.Errorf("%q: think = nil, want %v", val, *want)
		case want != nil && got != nil && *want != *got:
			t.Errorf("%q: think = %v, want %v", val, *got, *want)
		}
	}
}

func routeThinkPtr(b bool) *bool { return &b }
