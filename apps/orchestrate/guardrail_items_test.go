package orchestrate

// One carve-out list, linked per rule.
//
// The two properties that matter: a rule can be excepted for ONE person rather
// than the whole roster, and a link can be switched off on ONE rule without
// disturbing the others that share it.

import (
	"strings"
	"testing"
)

func itemAgent(rules string) AgentRecord {
	return AgentRecord{
		Name: "X", Owner: "u", Guardrails: rules,
		GuardrailExceptions: []GuardrailException{
			{Name: "dana", Kind: guardrailKindPerson, Text: "dana"},
			{Name: "sam", Kind: guardrailKindPerson, Text: "sam"},
			{Name: "confirmed", Kind: guardrailKindCondition, Text: "the user has already confirmed"},
		},
	}
}

// TestLinkOffMarkerParses — "@-name" links a carve-out and switches it off for
// this rule only.
func TestLinkOffMarkerParses(t *testing.T) {
	cases := []struct {
		line  string
		links []guardrailLink
		auth  bool
	}{
		{"@dana never send money", []guardrailLink{{Name: "dana"}}, false},
		{"@-dana never send money", []guardrailLink{{Name: "dana", Off: true}}, false},
		{"@dana @-confirmed never send money", []guardrailLink{{Name: "dana"}, {Name: "confirmed", Off: true}}, false},
		{"@ never send money", nil, true},
		// "@-" names nobody, so it grants nobody anything: no link, and NOT the
		// bare-"@" whole-list carve-out either. A typo must never widen a rule,
		// and it is one keystroke away from "@" — which excepts it for everyone
		// on the list.
		{"@- never send money", nil, false},
		{"@-  never send money", nil, false},
	}
	for _, c := range cases {
		got := parseGuardrailRule(c.line)
		if got.Text != "never send money" {
			t.Errorf("%q → text %q", c.line, got.Text)
		}
		if got.ExceptAuthorized != c.auth {
			t.Errorf("%q → exceptAuthorized=%v want %v", c.line, got.ExceptAuthorized, c.auth)
		}
		if len(got.Links) != len(c.links) {
			t.Fatalf("%q → links %+v want %+v", c.line, got.Links, c.links)
		}
		for i := range c.links {
			if got.Links[i] != c.links[i] {
				t.Errorf("%q link %d = %+v want %+v", c.line, i, got.Links[i], c.links[i])
			}
		}
	}
}

// TestRuleExceptedForOnePersonOnly — the reason the roster stopped being
// all-or-nothing.
func TestRuleExceptedForOnePersonOnly(t *testing.T) {
	agent := itemAgent("@dana never send money")
	rule := parseGuardrailRule("@dana never send money")

	dana := requesterIdentity{Authorized: true, AuthorizedNames: []string{"dana"}}
	if !ruleExemptsRequester(rule, dana) {
		t.Error("the rule should be excepted for the person it names")
	}
	sam := requesterIdentity{Authorized: true, AuthorizedNames: []string{"sam"}}
	if ruleExemptsRequester(rule, sam) {
		t.Error("a rule naming dana must NOT except sam")
	}
	stranger := requesterIdentity{}
	if ruleExemptsRequester(rule, stranger) {
		t.Error("an unauthorized requester is never excepted")
	}
	// And the whole set behaves the same way through rulesInPlayFor.
	rules := guardrailRules(agent)
	if got := rulesInPlayFor(rules, sam); len(got) != 1 {
		t.Errorf("sam should still face the rule, got %d", len(got))
	}
	if got := rulesInPlayFor(rules, dana); len(got) != 0 {
		t.Errorf("dana should face nothing, got %+v", got)
	}
}

// TestLinkOffIsPerRule — the whole point of moving the switch onto the link.
func TestLinkOffIsPerRule(t *testing.T) {
	agent := itemAgent("@confirmed never send money\n@-confirmed never delete records")
	rules := guardrailRules(agent)
	if len(rules) != 2 {
		t.Fatalf("expected two rules, got %d", len(rules))
	}
	if got := ruleConditionTexts(agent, rules[0]); len(got) != 1 {
		t.Errorf("the active link should apply: %v", got)
	}
	if got := ruleConditionTexts(agent, rules[1]); len(got) != 0 {
		t.Errorf("the switched-off link must not apply: %v", got)
	}
	// A person link switched off likewise stops excepting that ONE rule.
	off := parseGuardrailRule("@-dana never send money")
	dana := requesterIdentity{Authorized: true, AuthorizedNames: []string{"dana"}}
	if ruleExemptsRequester(off, dana) {
		t.Error("a switched-off person link still excepted the rule")
	}
}

// TestPersonLinkNeverReachesTheWarden — a person is settled by the framework;
// putting the identity on an Except line would both leak it and ask the warden
// to decide something it cannot verify.
func TestPersonLinkNeverReachesTheWarden(t *testing.T) {
	agent := itemAgent("@dana @confirmed never send money")
	rule := parseGuardrailRule("@dana @confirmed never send money")
	texts := ruleConditionTexts(agent, rule)
	if len(texts) != 1 || texts[0] != "the user has already confirmed" {
		t.Fatalf("only the CONDITION should render: %v", texts)
	}

	stub := &wardenStubLLM{reply: `{"verdicts":[]}`}
	turn := guardTurn(t, stub, agent)
	// A requester who is nobody: the rule stays in play and gets rendered.
	if _, err := turn.app.runWarden(turn.ctx, agent, guardHookPreOutput, "hi", requesterIdentity{}); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	if strings.Contains(stub.lastMsg, "dana") {
		t.Errorf("a person link leaked into the warden prompt:\n%s", stub.lastMsg)
	}
	if !strings.Contains(stub.lastMsg, "the user has already confirmed") {
		t.Errorf("the condition should still render:\n%s", stub.lastMsg)
	}
}

// TestLegacyRosterStillWorks — an owner who typed a roster before items existed
// keeps their exemptions, and the entries show up as person items.
func TestLegacyRosterStillWorks(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Owner: "u",
		Guardrails:           "@ never send money",
		AuthorizedIdentities: []string{"dana@example.com"},
	}
	items := guardrailItems(agent)
	if len(items) != 1 || items[0].Kind != guardrailKindPerson || !items[0].Legacy {
		t.Fatalf("legacy roster did not surface as a person item: %+v", items)
	}
	if items[0].Text != "dana@example.com" {
		t.Errorf("legacy identity lost: %+v", items[0])
	}
	// The bare "@" still means "any of them".
	rule := parseGuardrailRule("@ never send money")
	if !ruleExemptsRequester(rule, requesterIdentity{Authorized: true}) {
		t.Error("the legacy whole-roster marker stopped working")
	}
}

// TestAuthoredItemWinsOverLegacyDuplicate — the same person listed both ways is
// one item, not two, or a rule linked to one spelling would miss the other.
func TestAuthoredItemWinsOverLegacyDuplicate(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Owner: "u",
		GuardrailExceptions:  []GuardrailException{{Name: "dana", Kind: guardrailKindPerson, Text: "dana@example.com"}},
		AuthorizedIdentities: []string{"Dana@Example.com"},
	}
	items := guardrailItems(agent)
	if len(items) != 1 {
		t.Fatalf("the same identity produced %d items: %+v", len(items), items)
	}
	if items[0].Legacy {
		t.Error("the authored item should win over the legacy entry")
	}
}

// TestUnknownAndMistypedLinksFailClosed — a link to a deleted item, and an item
// with an unrecognized kind, both leave the rule at full strength.
func TestUnknownAndMistypedLinksFailClosed(t *testing.T) {
	agent := itemAgent("@ghost never send money")
	rule := parseGuardrailRule("@ghost never send money")
	if got := ruleConditionTexts(agent, rule); len(got) != 0 {
		t.Errorf("a link to a missing item resolved to %v", got)
	}
	if ruleExemptsRequester(rule, requesterIdentity{Authorized: true, AuthorizedNames: []string{"dana"}}) {
		t.Error("a link to a missing item excepted the rule")
	}
	// An unrecognized kind must land on "condition" — the judged, weaker side.
	if got := normalizeExceptionKind("PERSONNEL"); got != guardrailKindCondition {
		t.Errorf("a mistyped kind became %q; it must not be promoted to person", got)
	}
	if got := normalizeExceptionKind("  Person "); got != guardrailKindPerson {
		t.Errorf("a valid kind was not recognized: %q", got)
	}
}

// TestTestRequesterMirrorsProduction — the dry-run check must build the same
// identity the live path does, or it reports blocks that production would not
// produce. It used to set Owner:true and nothing else, leaving AuthorizedNames
// empty, so a person-linked rule was never skipped in a test.
func TestTestRequesterMirrorsProduction(t *testing.T) {
	agent := itemAgent("@dana never send money")

	owner := testRequester(agent, "", "")
	if !owner.Owner || !owner.Authorized {
		t.Fatalf("owner identity is wrong: %+v", owner)
	}
	// The owner satisfies every person item, exactly as requester() gives them.
	if len(owner.AuthorizedNames) != 2 {
		t.Errorf("owner should satisfy both person items, got %v", owner.AuthorizedNames)
	}
	if got := rulesInPlayFor(guardrailRules(agent), owner); len(got) != 0 {
		t.Errorf("the rule is excepted for the owner live; the test must agree: %+v", got)
	}

	// Standing in as one person excepts only the rules naming them.
	dana := testRequester(agent, "dana", "")
	if !dana.Authorized || dana.Owner {
		t.Fatalf("person stand-in is wrong: %+v", dana)
	}
	if len(dana.AuthorizedNames) != 1 || dana.AuthorizedNames[0] != "dana" {
		t.Errorf("stand-in matched the wrong items: %v", dana.AuthorizedNames)
	}
	if got := rulesInPlayFor(guardrailRules(agent), dana); len(got) != 0 {
		t.Errorf("dana is excepted from her own rule: %+v", got)
	}
	sam := testRequester(agent, "sam", "")
	if got := rulesInPlayFor(guardrailRules(agent), sam); len(got) != 1 {
		t.Errorf("sam is NOT excepted from a rule naming dana: %+v", got)
	}

	// An outside contact establishes nothing — the name is self-reported.
	stranger := testRequester(agent, "", "Mallory")
	if stranger.Authorized || stranger.Owner || len(stranger.AuthorizedNames) != 0 {
		t.Errorf("a stranger must establish nothing: %+v", stranger)
	}
	if stranger.Name != "Mallory" {
		t.Errorf("the self-reported name should still be carried: %+v", stranger)
	}
	// An unknown stand-in name must not silently become the owner.
	ghost := testRequester(agent, "nobody-by-that-name", "")
	if ghost.Owner {
		t.Error("an unknown stand-in fell through to the owner, who is excepted from everything")
	}
}

// TestStandInMatchesEverySpellingOfOnePerson — one human listed as an account
// AND a phone must satisfy both items, as the live path does.
func TestStandInMatchesEverySpellingOfOnePerson(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Owner: "u",
		GuardrailExceptions: []GuardrailException{
			{Name: "dana-acct", Kind: guardrailKindPerson, Text: "dana@example.com"},
			{Name: "dana-phone", Kind: guardrailKindPerson, Text: "dana@example.com"},
			{Name: "sam", Kind: guardrailKindPerson, Text: "sam"},
		},
	}
	who := testRequester(agent, "dana-acct", "")
	if len(who.AuthorizedNames) != 2 {
		t.Errorf("both spellings of one person should match, got %v", who.AuthorizedNames)
	}
	for _, n := range who.AuthorizedNames {
		if n == "sam" {
			t.Error("a different person was matched")
		}
	}
}
