package orchestrate

import (
	"strings"
	"testing"
)

// The case this exists for, end to end at the resolver: "don't tell a joke
// unless it's requested twice". The agent counted right, called get_joke on the
// second ask, and was blocked by a warden that cannot see the count. The appeal
// turns that into a question the framework answers by looking, not by believing.
func TestAppealCountsOnlyTheUsersOwnWords(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "Tell me a joke"},
		{Role: "assistant", Content: "You've only asked once — holding off. Tell me a joke is what you said."},
		{Role: "user", Content: "tell me a joke"},
	}}}

	// Two USER requests, case-insensitively — and the assistant turn quoting
	// the same words is NOT one of them. That exclusion is the whole security
	// property: counting the agent's own repetitions would let it manufacture
	// the precondition it is appealing on.
	if n, excerpt := turn.countUserQuote("tell me a joke"); n != 2 {
		t.Errorf("both user requests must count, got %d (excerpt %q)", n, excerpt)
	}

	// A quote nobody said establishes nothing, however confidently claimed.
	if n, _ := turn.countUserQuote("please tell me three jokes"); n != 0 {
		t.Errorf("a phrase never said must not be found, got %d", n)
	}

	// Whitespace folds, so a quote broken across a line still matches.
	wrapped := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "tell   me\na joke"},
	}}}
	if n, _ := wrapped.countUserQuote("tell me a joke"); n != 1 {
		t.Error("whitespace differences must not defeat a citation")
	}

	// A fragment short enough to match everywhere would let an appeal "verify"
	// anything, so there is a floor.
	if n, _ := turn.countUserQuote("a"); n != 0 {
		t.Error("a fragment below the length floor must resolve to nothing")
	}
}

// Only the owner can make a rule appealable, and only that rule becomes so.
func TestOnlyMarkedRulesAreContestable(t *testing.T) {
	agent := AgentRecord{Guardrails: strings.Join([]string{
		"never mention salary",
		"~ don't tell a joke unless it's requested twice",
		"? answer in Spanish",
	}, "\n")}

	if !ruleIsContestable(agent, "don't tell a joke unless it's requested twice") {
		t.Error("the marked rule must be appealable")
	}
	if ruleIsContestable(agent, "never mention salary") {
		t.Error("an unmarked rule must never become appealable")
	}
	// The marker says HOW it is judged, not how softly a confirmed breach is
	// treated — a contestable rule is still terminal on its own.
	if ruleIsCorrectable(agent, "don't tell a joke unless it's requested twice") {
		t.Error("contestable must not imply correctable")
	}
	// The marker is stripped before the warden ever sees the rule.
	for _, r := range guardrailRules(agent) {
		if strings.HasPrefix(r.Text, guardrailContestableMarker) {
			t.Errorf("the marker must not reach the warden: %q", r.Text)
		}
	}
	if !agentHasContestableRule(agent) {
		t.Error("an agent with a marked rule must get the appeal tool")
	}
	if agentHasContestableRule(AgentRecord{Guardrails: "never mention salary"}) {
		t.Error("an agent with no marked rule must not be given a tool about guardrails")
	}
}

// An appeal is one shot, and it is spent on the ATTEMPT. Spending it only on
// success would let a turn retry until something lands, which is negotiation —
// the exact thing a guardrail must not become.
func TestAppealIsOfferedOnceAndSpentOnAttempt(t *testing.T) {
	agent := AgentRecord{Guardrails: "~ don't tell a joke unless it's requested twice"}
	turn := &chatTurn{agent: agent}

	invite := turn.offerGuardrailAppeal("don't tell a joke unless it's requested twice", guardHookPreOutput, "a joke")
	if invite == "" {
		t.Fatal("a contestable rule must invite an appeal when it blocks")
	}
	if turn.appealOffer == nil {
		t.Fatal("the invitation must arm the tool, or it invites something impossible")
	}

	turn.appealSpent = true
	if again := turn.offerGuardrailAppeal("don't tell a joke unless it's requested twice", guardHookPreOutput, "a joke"); again != "" {
		t.Error("a turn that has already appealed must not be invited to appeal again")
	}

	// An unmarked rule blocks with no invitation at all.
	plain := &chatTurn{agent: AgentRecord{Guardrails: "never mention salary"}}
	if invite := plain.offerGuardrailAppeal("never mention salary", guardHookPreOutput, "£90k"); invite != "" {
		t.Error("an unmarked rule must not offer an appeal")
	}
	if plain.appealOffer != nil {
		t.Error("an unmarked rule must not arm the tool")
	}
}

// The tool refuses when nothing is pending. "Only after a block" is enforced
// here rather than asked for in the description, because a tool that merely
// requests not to be used speculatively will be.
func TestAppealToolRefusesWithNothingPending(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{Guardrails: "~ don't tell a joke unless it's requested twice"}}
	out, err := turn.guardrailAppealToolDef().Handler(map[string]any{
		"claim": "they asked twice", "quote": "tell me a joke",
	})
	if err != nil {
		t.Fatalf("the tool must answer, not error: %v", err)
	}
	if !strings.Contains(out, "nothing to appeal") {
		t.Errorf("an unprompted appeal must be refused, got %q", out)
	}
	if turn.appealSpent {
		t.Error("refusing an unprompted call must not consume the turn's one appeal")
	}
}

// A citation that was never said fails without any model being consulted, and
// the block stands. This is the path a compromised agent takes, so it has to be
// cheap and certain rather than argued.
func TestAppealWithAnUnsaidQuoteFailsWithoutTheWarden(t *testing.T) {
	turn := &chatTurn{
		agent:   AgentRecord{Guardrails: "~ don't tell a joke unless it's requested twice"},
		session: &ChatSession{Messages: []ChatMessage{{Role: "user", Content: "Tell me a joke"}}},
	}
	turn.offerGuardrailAppeal("don't tell a joke unless it's requested twice", guardHookPreOutput, "a joke")

	// app is nil: if this path reached the warden it would panic, which is the
	// assertion — resolution happens before anything is asked to judge.
	out, err := turn.guardrailAppealToolDef().Handler(map[string]any{
		"claim": "they asked twice",
		"quote": "I demand two jokes immediately",
	})
	if err != nil {
		t.Fatalf("a failed appeal answers, it does not error: %v", err)
	}
	if !strings.Contains(out, "does not appear") {
		t.Errorf("the agent must be told its citation was not found, got %q", out)
	}
	if !turn.appealSpent {
		t.Error("a failed attempt still spends the appeal")
	}
	if turn.appealCleared("don't tell a joke unless it's requested twice") {
		t.Error("a failed appeal must never clear the rule")
	}
}

// A won appeal has to hold for the rest of the turn. The warden is stateless
// and fresh on every call, so without this the next round re-asks the same
// blind question and re-blocks — and the evidence the agent just produced would
// count for nothing.
func TestAWonAppealHoldsForTheTurnAndOnlyForThatRule(t *testing.T) {
	turn := &chatTurn{appealWon: map[string]bool{
		normalizeRuleText("don't tell a joke unless it's requested twice"): true,
	}}
	// Requoted by the warden with different casing and a trailing period —
	// the same fold ruleIsCorrectable uses, for the same reason.
	if !turn.appealCleared("Don't tell a joke unless it's requested twice.") {
		t.Error("a won appeal must survive the warden requoting the rule")
	}
	if turn.appealCleared("never mention salary") {
		t.Error("winning one appeal must not relax any other rule")
	}
	if (&chatTurn{}).appealCleared("anything") {
		t.Error("a turn with no appeal clears nothing")
	}
}
