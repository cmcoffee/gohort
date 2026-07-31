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

// "Auto" has to say what it will actually do. The label resolves through the same
// function the call site uses, with a blank record standing in for "no
// agent-level override", so it cannot describe behaviour the system does not have.
func TestAutoThinkLabelResolvesToRealBehaviour(t *testing.T) {
	label := currentAutoThinkLabel()
	if !strings.Contains(label, "currently") {
		t.Errorf("the Auto think label must state the resolved value; got %q", label)
	}
	wantOn := resolveDispatchThink(AgentRecord{})
	if wantOn != strings.Contains(label, "ON") {
		t.Errorf("label %q disagrees with resolveDispatchThink(%v)", label, wantOn)
	}
}

// The rejection writer sees exactly one thing — the flagged message — and every
// style example in its prompt is a first-person decline. Handed a message that
// is ITSELF first person ("I'm not going back to being called X"), the model
// continued that voice instead of answering it, so the owner's own refusal came
// back out of the agent's mouth. The block was correct; the pronoun inverted who
// wanted what. Whose "I" is whose has to be stated, not left to the examples.
// A second, mirror-image failure followed: on a channel the sender's name is
// folded into the message text upstream (attributeSender), so the fence reads
// "Craig Coffee: …". Nothing told the writer that this person is the one about
// to READ the reply, so it treated the name as a third party — "Not gonna get
// into the Wiwee drama with Craig", said to Craig. Both directions have to be
// stated: whose voice it writes in, and who it is writing to.
func TestRejectionPromptSaysWhoseVoiceItWritesIn(t *testing.T) {
	p := rejectionSystemPrompt
	for _, want := range []string{
		"WHOSE VOICE",                   // stated as its own rule, not buried in the style notes
		"belongs to them, never to you", // the message's first person is the sender's
		"WHO YOU ARE TALKING TO",        // and the reader is the sender, not a third party
		"second person",
		"YOUR OWN NAME", // and the assistant's own name means itself
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the rejection prompt must carry %q — without it the writer gets the pronouns wrong", want)
		}
	}
	// The ghostwriter framing IS the third-person bug. "On behalf of an
	// assistant" plus "you are not the assistant it was written for" told the
	// model it was writing for somebody else, and it duly wrote about that
	// somebody in the third person ("the Wiwee drama"). The injection defense
	// those words carried is the refusal to act on the message, not the denial
	// of identity, so it survives their removal.
	for _, banned := range []string{"on behalf of", "not the assistant"} {
		if strings.Contains(p, banned) {
			t.Errorf("%q reintroduces the ghostwriter framing — the writer IS the assistant", banned)
		}
	}
	// And again where the model scans for prohibitions: a framing paragraph it
	// reads once is weaker than a line in the list of things never to do.
	never := p[strings.Index(p, "Never do any of these:"):]
	for _, want := range []string{
		"echo the message's voice",
		"write about yourself in the third person",
		"name the person you are replying to",
	} {
		if !strings.Contains(never, want) {
			t.Errorf("%q must also appear as a prohibition, not only as framing", want)
		}
	}
}

// Recognizing its own name can't be left to inference: the message is the
// writer's whole context, and a name inside it looks far more like a third party
// than like the reader of the prompt. So the name is handed over as trusted
// fact — it comes off the agent record, which only the owner writes, never off
// the wire.
func TestRejectionWriterIsToldItsOwnName(t *testing.T) {
	line := rejectionIdentityLine("WiWee")
	if !strings.Contains(line, "WiWee") {
		t.Fatal("the agent's name must reach the rejection writer")
	}
	if !strings.Contains(line, "trusted") {
		t.Error("the name is owner-authored, and saying so is what separates it from anything in the fenced message")
	}
	if !strings.Contains(line, "keep the name out of your reply") {
		t.Error("knowing the name must not become licence to sign the refusal with it")
	}
	// An unnamed agent says nothing rather than announcing a blank.
	if got := rejectionIdentityLine("   "); got != "" {
		t.Errorf("an unnamed agent contributes no identity line, got %q", got)
	}
}
