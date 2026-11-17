package anvil

import "testing"

// A detached command is launched and not waited for: it has no timeout and
// is never offered to the assistant, and the hint is read only for a command
// the assistant may run. The row shows only what applies to it.
func TestACommandRowShowsOnlyWhatApplies(t *testing.T) {
	want := map[string]string{
		"timeout_sec":     "!detached",
		"expose_to_agent": "!detached",
		"agent_hint":      "expose_to_agent;!detached",
	}
	seen := map[string]bool{}
	for _, c := range commandsField().Columns {
		if gate, ok := want[c.Field]; ok {
			seen[c.Field] = true
			if c.ShowWhen != gate {
				t.Errorf("%s: ShowWhen = %q, want %q", c.Field, c.ShowWhen, gate)
			}
		}
	}
	for f := range want {
		if !seen[f] {
			t.Errorf("the %s column is gone", f)
		}
	}
	// The runtime side of the same rule: a detached command is never
	// offered, whatever expose_to_agent says.
	p := Project{Actions: []ProjectAction{
		{ID: "a", Label: "Restart", Command: "true", Detached: true, ExposeToAgent: true},
		{ID: "b", Label: "Test", Command: "true", ExposeToAgent: true},
	}}
	got := p.agentInvokableActions()
	if len(got) != 1 || got[0].ID != "b" {
		t.Errorf("only the non-detached exposed command should be offered, got %+v", got)
	}
}
