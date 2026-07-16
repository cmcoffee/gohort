package orchestrate

import "testing"

// effectiveDispatchMode must apply back-compat: a blank mode with a non-empty
// target list is the legacy allowlist ("only"), and an unknown mode fails OPEN
// to the same inference (never a silent hard block).
func TestEffectiveDispatchMode(t *testing.T) {
	cases := []struct {
		mode    string
		targets []string
		want    string
	}{
		{"", nil, dispatchAll},                  // default
		{"", []string{"a"}, dispatchOnly},       // legacy allowlist inference
		{"all", []string{"a"}, dispatchAll},     // explicit all wins over a list
		{"only", nil, dispatchOnly},             // explicit
		{"except", []string{"a"}, dispatchExcept},
		{"none", nil, dispatchNone},
		{"bogus", []string{"a"}, dispatchOnly},  // unknown → fail-open, legacy infer
		{"bogus", nil, dispatchAll},             // unknown, no list → all
	}
	for _, c := range cases {
		got := effectiveDispatchMode(AgentRecord{DispatchMode: c.mode, AllowedDispatchTargets: c.targets})
		if got != c.want {
			t.Errorf("mode=%q targets=%v → %q, want %q", c.mode, c.targets, got, c.want)
		}
	}
}
