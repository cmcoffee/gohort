package orchestrate

// Effort is the everyday reasoning control on an agent. The CRUD tools accept
// it, it takes precedence over the legacy think on/off/auto, and every run
// path reads the agent's think mode through it.

import "testing"

func TestCreateAgentAcceptsEffort(t *testing.T) {
	rec := agentRecordFromArgs(map[string]any{
		"name": "Fresh", "orchestrator_prompt": "p", "effort": "High", "think": "off",
	})
	if rec.Effort != "high" {
		t.Errorf("effort = %q, want high", rec.Effort)
	}
	// Effort wins over the think param passed beside it.
	if rec.Think != "on" || rec.thinkMode() != "on" {
		t.Errorf("think = %q (mode %q): a level above off means reasoning on", rec.Think, rec.thinkMode())
	}
	off := agentRecordFromArgs(map[string]any{"name": "Quiet", "orchestrator_prompt": "p", "effort": "off"})
	if off.Effort != "off" || off.Think != "off" {
		t.Errorf("effort off: effort=%q think=%q", off.Effort, off.Think)
	}
	// Not passed: nothing changes from the old behaviour.
	plain := agentRecordFromArgs(map[string]any{"name": "Plain", "orchestrator_prompt": "p"})
	if plain.Effort != "" || plain.Think != "on" {
		t.Errorf("no effort: effort=%q think=%q", plain.Effort, plain.Think)
	}
}

func TestUpdateAgentAcceptsEffort(t *testing.T) {
	rec := AgentRecord{ID: "a", Think: "off"}
	mergeAgentArgs(&rec, map[string]any{"effort": "medium"})
	if rec.Effort != "medium" || rec.Think != "on" {
		t.Errorf("effort=%q think=%q, want medium / on", rec.Effort, rec.Think)
	}
	// Omitted keeps the stored value; a typo does too.
	mergeAgentArgs(&rec, map[string]any{"name": "renamed"})
	mergeAgentArgs(&rec, map[string]any{"effort": "extreme"})
	if rec.Effort != "medium" {
		t.Errorf("an omitted or invalid effort changed the stored value to %q", rec.Effort)
	}
	// think passed beside effort: effort decides.
	mergeAgentArgs(&rec, map[string]any{"think": "off", "effort": "low"})
	if rec.Think != "on" || rec.Effort != "low" {
		t.Errorf("think=%q effort=%q, want effort to take precedence", rec.Think, rec.Effort)
	}
	// "default" clears it on purpose.
	mergeAgentArgs(&rec, map[string]any{"effort": "default"})
	if rec.Effort != "" {
		t.Errorf("default did not clear effort: %q", rec.Effort)
	}
}

// An agent edited in the form can hold Think "off" with an effort level; the
// run paths read thinkMode, so the level still decides.
func TestEffortDecidesTheThinkMode(t *testing.T) {
	for _, c := range []struct{ think, effort, want string }{
		{"off", "high", "on"},
		{"on", "off", "off"},
		{"", "low", "on"},
		{"off", "", "off"},
		{"", "", ""},
	} {
		if got := (AgentRecord{Think: c.think, Effort: c.effort}).thinkMode(); got != c.want {
			t.Errorf("think=%q effort=%q: mode %q, want %q", c.think, c.effort, got, c.want)
		}
	}
	if !resolveDispatchThink(AgentRecord{Think: "off", Effort: "medium"}) {
		t.Error("a dispatched agent at effort medium did not reason")
	}
}

func TestEffortIsOnEveryAgentSurface(t *testing.T) {
	if _, ok := agentMutationParams(false)["effort"]; !ok {
		t.Error("create_agent has no effort param")
	}
	if _, ok := agentMutationParams(true)["effort"]; !ok {
		t.Error("update_agent has no effort param")
	}
	if !patchAgentFields["effort"] {
		t.Error("the agent editor's partial save drops effort")
	}
}
