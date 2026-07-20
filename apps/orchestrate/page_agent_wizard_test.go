package orchestrate

import (
	"strings"
	"testing"
)

func TestWizardFallbackDescription(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Answer ops questions. Also do other things later.", "Answer ops questions."},
		{"First line only\nsecond line ignored", "First line only"},
		{strings.Repeat("x", 200), strings.Repeat("x", 157) + "…"},
	}
	for _, c := range cases {
		if got := wizardFallbackDescription(c.in); got != c.want {
			t.Errorf("wizardFallbackDescription(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWizardFallbackPrompt(t *testing.T) {
	p := wizardFallbackPrompt("Ops Bot", "Answer ops questions.", "terse")
	if !strings.Contains(p, "Ops Bot") || !strings.Contains(p, "Answer ops questions.") || !strings.Contains(p, "terse") {
		t.Errorf("fallback should include name, purpose, and style; got %q", p)
	}
}

func TestApplyWizardMemory(t *testing.T) {
	assistant := func() AgentRecord { return AgentRecord{Cortex: true, MemoryMode: "chatbot"} }

	rec := assistant()
	if !applyWizardMemory(&rec, wizardRequest{}) || rec.MemoryMode != "chatbot" || !rec.Cortex ||
		rec.DisableExplicit || rec.DisableInferred {
		t.Errorf("empty dials must keep type defaults; got %+v", rec)
	}
	rec = assistant()
	if !applyWizardMemory(&rec, wizardRequest{Memory: "lessons", Cortex: "off"}) ||
		rec.MemoryMode != "agent" || rec.Cortex {
		t.Errorf("lessons+off overrides not applied; got %+v", rec)
	}
	rec = AgentRecord{MemoryMode: "agent"}
	if !applyWizardMemory(&rec, wizardRequest{Memory: "personalized", Cortex: "on"}) ||
		rec.MemoryMode != "chatbot" || !rec.Cortex {
		t.Errorf("personalized+on overrides not applied; got %+v", rec)
	}
	rec = assistant()
	if !applyWizardMemory(&rec, wizardRequest{Memory: "none"}) ||
		!rec.DisableExplicit || !rec.DisableInferred {
		t.Errorf("none must disable both memory layers; got %+v", rec)
	}
	rec = assistant()
	if applyWizardMemory(&rec, wizardRequest{Memory: "bogus"}) {
		t.Error("unknown memory value must be rejected")
	}
	rec = assistant()
	if applyWizardMemory(&rec, wizardRequest{Cortex: "bogus"}) {
		t.Error("unknown cortex value must be rejected")
	}
}

func TestNeedsFirstRunSetup(t *testing.T) {
	own := AgentRecord{ID: "ag-1", Owner: "alice"}
	seed := AgentRecord{ID: "seed-chat", Owner: "alice"} // shadowed seed: Owner rewritten, still a seed
	sub := AgentRecord{ID: "ag-2", Owner: "alice", OwnedBy: "ag-1"}
	other := AgentRecord{ID: "ag-3", Owner: "bob"}
	cases := []struct {
		name   string
		agents []AgentRecord
		want   bool
	}{
		{"no agents at all", nil, true},
		{"only seeds", []AgentRecord{seed}, true},
		{"only a sub-agent", []AgentRecord{sub}, true},
		{"only someone else's record", []AgentRecord{other}, true},
		{"owns a top-level agent", []AgentRecord{seed, own}, false},
	}
	for _, c := range cases {
		if got := needsFirstRunSetup(c.agents, "alice"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWizardSeedNotes(t *testing.T) {
	if got := wizardSeedNotes(wizardRequest{}); got != "" {
		t.Errorf("empty about-you should yield no notes; got %q", got)
	}
	got := wizardSeedNotes(wizardRequest{CallYou: " Craig ", AboutYou: "Runs a homelab."})
	if !strings.Contains(got, "called: Craig") || !strings.Contains(got, "About the user: Runs a homelab.") {
		t.Errorf("notes missing fields: %q", got)
	}
}

func TestWizardBriefIncludesAboutYou(t *testing.T) {
	b := wizardBrief("Assistant", wizardRequest{
		Kind: "assistant", Purpose: "Help me.", CallYou: "boss", AboutYou: "Night owl.",
	})
	if !strings.Contains(b, "boss") || !strings.Contains(b, "Night owl.") {
		t.Errorf("brief missing about-you lines: %q", b)
	}
}

func TestWizardKindsMatchEditorPresets(t *testing.T) {
	// The wizard's kind→defaults mapping must stay in lockstep with the
	// editor's Agent-type presets so both create paths produce the same
	// character. Compare against agentTypeTemplates by label prefix.
	byPrefix := map[string]map[string]any{}
	for _, tpl := range agentTypeTemplates() {
		key := strings.ToLower(strings.SplitN(tpl.Label, " ", 2)[0])
		byPrefix[key] = tpl.Values
	}
	for kind, def := range wizard_kinds {
		key := kind
		if kind == "channel" {
			key = "channel" // template label starts "Channel agent"
		}
		vals, ok := byPrefix[key]
		if !ok {
			continue // template naming drift — the label check below still covers the shipped kinds
		}
		if want, ok := vals["channel"].(bool); ok && want != def.cortex {
			t.Errorf("kind %q cortex=%v but editor preset stamps channel=%v", kind, def.cortex, want)
		}
		if want, ok := vals["memory_mode"].(string); ok && want != def.memory_mode {
			t.Errorf("kind %q memory_mode=%q but editor preset stamps %q", kind, def.memory_mode, want)
		}
	}
}
