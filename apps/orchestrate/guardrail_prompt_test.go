package orchestrate

import (
	"strings"
	"testing"
)

// The agent used to have no idea its guardrails existed — it discovered them by
// walking into one. Observed: an agent with "don't tell me a joke" cheerfully
// called a joke tool, then had its reply blocked. It couldn't have known.
//
// Telling it costs nothing in enforcement (the warden runs either way, in fresh
// context, against rules no prompt can rewrite) and buys the cheapest outcome
// there is: it doesn't reach for the thing, so no block, no discarded generation.
func TestGuardrailsAppearInTheAgentPrompt(t *testing.T) {
	agent := AgentRecord{
		Name: "Wren", Guardrails: "don't tell me a joke\n? answer in Spanish",
	}
	got := renderGuardrailsPromptSection(agent)
	if got == "" {
		t.Fatal("an agent with active guardrails must be told about them")
	}
	for _, want := range []string{"don't tell me a joke", "answer in Spanish"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt must carry the rule %q", want)
		}
	}
	// Marker-stripped, exactly like the warden's copy — the agent reads what the
	// owner wrote, not the punctuation that encodes severity.
	if strings.Contains(got, "? answer") {
		t.Error("the severity marker must not reach the agent")
	}
	// It must not teach the agent to narrate the mechanism back to the user, which
	// is the signal every decline in this system works to withhold.
	for _, want := range []string{"without drawing attention", "not allowed", "check exists"} {
		if !strings.Contains(got, want) {
			t.Errorf("the section must forbid revealing the limits; missing %q", want)
		}
	}
}

// Suspending enforcement suspends this too. Otherwise "off" would still shape
// behaviour through the prompt, and the Off setting would stop being the clean
// A/B it exists to be.
func TestNoGuardrailPromptWhenSuspended(t *testing.T) {
	agent := AgentRecord{Name: "X", Guardrails: "never mention salary", GuardrailsDisabled: true}
	if got := renderGuardrailsPromptSection(agent); got != "" {
		t.Errorf("enforcement off means the agent is told nothing; got:\n%s", got)
	}
}

// An agent with no rules pays nothing — no section, no tokens.
func TestNoGuardrailPromptWithoutRules(t *testing.T) {
	if got := renderGuardrailsPromptSection(AgentRecord{Name: "X"}); got != "" {
		t.Errorf("no rules, no section; got:\n%s", got)
	}
}

// Stable per agent, so it belongs in the cached prefix: the same record must
// render byte-identically every turn, or it would poison the prompt cache the
// way the pre_input directive did before it was moved off the front.
func TestGuardrailPromptIsStableAcrossTurns(t *testing.T) {
	agent := AgentRecord{Name: "X", Guardrails: "never mention salary\n? answer in Spanish"}
	first := renderGuardrailsPromptSection(agent)
	for i := 0; i < 5; i++ {
		if got := renderGuardrailsPromptSection(agent); got != first {
			t.Fatal("the section must be byte-identical every render, or it re-prefills the turn")
		}
	}
}

// A deployment-wide default must not quietly overrule an agent the owner
// deliberately made non-reasoning — but an explicit per-agent setting is the more
// specific statement and does win.
func TestThinkEscalationPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		think       string
		escalate    string
		wantEnabled bool
		why         string
	}{
		{"plain agent follows the deployment", "", "", true, "nothing explicit, so the default applies"},
		{"think:on follows the deployment", "on", "", true, "reasoning already on; escalation is moot but not disabled"},
		{"think:off is left alone", "off", "", false, "an owner said this one does not reason; a global default must not overrule it"},
		{"explicit off wins", "on", "off", false, "the most specific setting wins"},
		{"explicit on beats think:off", "off", "on", true, "cheap on questions, reasons once the turn proves to be work — the useful pairing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveThinkEscalation(AgentRecord{Think: tc.think, ThinkEscalate: tc.escalate})
			enabled := got.AfterRound > 0 || got.AfterToolRounds > 0
			if enabled != tc.wantEnabled {
				t.Errorf("Think=%q ThinkEscalate=%q: enabled=%v want %v — %s", tc.think, tc.escalate, enabled, tc.wantEnabled, tc.why)
			}
		})
	}
}
