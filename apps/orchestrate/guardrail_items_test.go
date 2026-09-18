package orchestrate

// One carve-out list of CONDITIONS, linked per rule.
//
// Identity is not in here. A rule yields to a person through the roster and the
// "@" marker, which the framework settles itself; an exception is prose the
// check reads. Keeping the two apart is the point: when both lived in this list
// a rule linking by name could reach either, and a name that appeared in the
// rule as well read to the judge as the rule's own subject.
//
// The property that remains: a link can be switched off on ONE rule without
// disturbing the others that share it.

import (
	"os"
	"strings"
	"testing"
)

func itemAgent(rules string) AgentRecord {
	return AgentRecord{
		Name: "X", Owner: "u", Guardrails: rules,
		AuthorizedIdentities: []string{"dana", "sam"},
		GuardrailExceptions: []GuardrailException{
			{Name: "confirmed", Text: "the user has already confirmed"},
			{Name: "night", Text: "it is outside business hours"},
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
func TestOnlyTheRosterMarkerExceptsAPerson(t *testing.T) {
	// "@" yields to anyone the framework established as authorized.
	authorized := requesterIdentity{Authorized: true}
	if !ruleExemptsRequester(parseGuardrailRule("@ never send money"), authorized) {
		t.Error("the roster marker should except an authorized requester")
	}
	if ruleExemptsRequester(parseGuardrailRule("never delete records"), authorized) {
		t.Error("an unmarked rule must apply to everyone, authorized or not")
	}
	if ruleExemptsRequester(parseGuardrailRule("@ never send money"), requesterIdentity{}) {
		t.Error("an unauthorized requester is never excepted")
	}

	// A link to a CONDITION never exempts anybody deterministically — that is
	// the judge's call, and the whole reason conditions render as text.
	linked := itemAgent("@confirmed never send money")
	rule := parseGuardrailRule("@confirmed never send money")
	if ruleExemptsRequester(rule, authorized) {
		t.Error("a condition link skipped the check instead of being judged")
	}
	if got := rulesInPlayFor(guardrailRules(linked), authorized); len(got) != 1 {
		t.Errorf("the rule should still be asked about, got %+v", got)
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
}

// TestTheRosterNeverReachesTheWarden — identity is settled by the framework;
// putting it on an Except line would both leak it and ask the warden to decide
// something it cannot verify.
func TestTheRosterNeverReachesTheWarden(t *testing.T) {
	agent := itemAgent("@ @confirmed never send money")
	rule := parseGuardrailRule("@ @confirmed never send money")
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
	// Nobody on the roster is named in the prompt. This is the property the
	// whole redesign rests on: a name the judge can read is a name it can
	// misread, and "dana" under a rule ABOUT dana reads as the rule's subject.
	for _, who := range []string{"dana", "sam"} {
		if strings.Contains(strings.ToLower(stub.seen()), who) {
			t.Errorf("a roster identity leaked into the warden prompt:\n%s", stub.seen())
		}
	}
	if !strings.Contains(stub.seen(), "the user has already confirmed") {
		t.Errorf("the condition should still render:\n%s", stub.seen())
	}
}

// TestTheRosterConfersAuthorizationOnItsOwn — the roster is an identity list
// and nothing else now. It used to also materialize as person items on every
// read, which is what made a deleted exception come back: the item went, the
// roster entry behind it did not, and the next read rebuilt it.
func TestTheRosterConfersAuthorizationOnItsOwn(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Owner: "u",
		Guardrails:           "@ never send money",
		AuthorizedIdentities: []string{"dana@example.com"},
	}
	// It contributes no carve-outs.
	if items := guardrailItems(agent); len(items) != 0 {
		t.Fatalf("the roster materialized as carve-outs again: %+v", items)
	}
	// And it still confers authorization, which is what "@" consults.
	if got := authorizedIdentities(agent); len(got) != 1 || got[0] != "dana@example.com" {
		t.Fatalf("roster = %v", got)
	}
	rule := parseGuardrailRule("@ never send money")
	if !ruleExemptsRequester(rule, requesterIdentity{Authorized: true}) {
		t.Error("the roster marker stopped working")
	}
}

// TestADeletedExceptionStaysDeleted — the bug this redesign came from. An
// exception removed from the authored list must not be rebuilt from anywhere.
func TestADeletedExceptionStaysDeleted(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Owner: "u",
		AuthorizedIdentities: []string{"craig@example.com"},
		GuardrailExceptions:  []GuardrailException{{Name: "confirmed", Text: "already confirmed"}},
	}
	agent.GuardrailExceptions = nil // the owner deletes it
	if items := guardrailItems(agent); len(items) != 0 {
		t.Fatalf("a deleted exception came back: %+v", items)
	}
}

// TestUnknownLinksFailClosed — a link to a deleted exception leaves the rule at
// full strength, which is the direction every unresolvable thing here fails in.
func TestUnknownLinksFailClosed(t *testing.T) {
	agent := itemAgent("@ghost never send money")
	rule := parseGuardrailRule("@ghost never send money")
	if got := ruleConditionTexts(agent, rule); len(got) != 0 {
		t.Errorf("a link to a missing exception resolved to %v", got)
	}
	if ruleExemptsRequester(rule, requesterIdentity{Authorized: true}) {
		t.Error("a link to a missing exception excepted the rule")
	}
}

// TestTestRequesterMirrorsProduction — the dry-run check must build the same
// identity the live path does, or it reports blocks production would not
// produce. It matches the ROSTER the same way, with the same whole-string
// compare, so a first name that is not on the roster resolves to nobody here
// exactly as it does live.
func TestTestRequesterMirrorsProduction(t *testing.T) {
	agent := itemAgent("@ never send money")

	owner := testRequester(agent, "", "")
	if !owner.Owner || !owner.Authorized {
		t.Fatalf("owner identity is wrong: %+v", owner)
	}
	if got := rulesInPlayFor(guardrailRules(agent), owner); len(got) != 0 {
		t.Errorf("the rule is excepted for the owner live; the test must agree: %+v", got)
	}

	// Standing in as somebody ON the roster.
	dana := testRequester(agent, "dana", "")
	if !dana.Authorized || dana.Owner {
		t.Fatalf("roster stand-in is wrong: %+v", dana)
	}
	if got := rulesInPlayFor(guardrailRules(agent), dana); len(got) != 0 {
		t.Errorf("an authorized person faces an @-marked rule: %+v", got)
	}

	// An outside contact establishes nothing — the name is self-reported.
	stranger := testRequester(agent, "", "Mallory")
	if stranger.Authorized || stranger.Owner {
		t.Errorf("a stranger must establish nothing: %+v", stranger)
	}
	if stranger.Name != "Mallory" {
		t.Errorf("the self-reported name should still be carried: %+v", stranger)
	}
	// An unknown stand-in must not silently become the owner, who is excepted
	// from everything — that would report "nothing blocks" for a person who
	// does not exist.
	ghost := testRequester(agent, "nobody-by-that-name", "")
	if ghost.Owner || ghost.Authorized {
		t.Error("an unknown stand-in was treated as somebody")
	}
	if got := rulesInPlayFor(guardrailRules(agent), ghost); len(got) != 1 {
		t.Errorf("an unknown stand-in should face every rule: %+v", got)
	}
}

// TestAFirstNameIsNotOnTheRoster — the trap behind the bug report. The roster
// is matched whole, so "Craig" is not "Craig Coffee" and an exemption written
// that way silently does nothing.
func TestAFirstNameIsNotOnTheRoster(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Owner: "u", Guardrails: "@ never say that",
		AuthorizedIdentities: []string{"Craig Coffee"},
	}
	if who := testRequester(agent, "Craig", ""); who.Authorized {
		t.Error("a first name matched a full account — the compare is whole-string on purpose")
	}
	if who := testRequester(agent, "craig coffee", ""); !who.Authorized {
		t.Error("the roster compare should ignore case")
	}
}

// TestTheJudgeIsToldWhatTheCandidateIs — the warden prompt has to say that the
// candidate is the AGENT'S output, not the requester's words.
//
// Observed live: an exception reading "Craig may bypass this rule" was flagged
// as a violation while the requester WAS Craig, authenticated. The judge's own
// reasoning was that the candidate arrives fenced as untrusted with no
// attribution, so it could not establish the text came "directly from him" and
// chose the safe answer under doubt. Correct reasoning, wrong question: the
// candidate is never the requester speaking.
func TestTheJudgeIsToldWhatTheCandidateIs(t *testing.T) {
	agent := AgentRecord{
		Name: "X", Owner: "u", Guardrails: "@confirmed never say that",
		GuardrailExceptions: []GuardrailException{{Name: "confirmed", Text: "the user has already confirmed"}},
	}
	stub := &wardenStubLLM{reply: `{"verdicts":[]}`}
	turn := guardTurn(t, stub, agent)
	who := requesterIdentity{Authorized: true, AuthorizedAs: "Craig Coffee", AuthorizedVia: guardAuthAuthenticated}
	if _, err := turn.app.runWarden(turn.ctx, agent, guardHookPreOutput, "some draft reply", who); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	seen := stub.seen()
	if !strings.Contains(seen, "AGENT'S OWN candidate") {
		t.Errorf("the prompt does not say whose output is being judged:\n%s", seen)
	}
	// And it must point a who-is-asking condition at the requester line rather
	// than at the candidate, which is where the misread happened.
	if !strings.Contains(seen, "settled by the REQUESTER line") {
		t.Errorf("the prompt does not say where an identity condition is settled:\n%s", seen)
	}
}

// TestPreInputIsToldTheCandidateIsTheRequestersOwnWords — the same line, the
// opposite fact, and getting it wrong is worse than saying nothing.
//
// pre_input judges the REQUESTER'S incoming message; every other hook judges
// what the agent is about to say. Telling the judge "this is the agent's
// output" at pre_input would assert that the person's own words were the
// agent's, which inverts the question any exception about who is asking
// depends on.
func TestPreInputIsToldTheCandidateIsTheRequestersOwnWords(t *testing.T) {
	agent := AgentRecord{Name: "X", Owner: "u", Guardrails: "never say that"}
	stub := &wardenStubLLM{reply: `{"verdicts":[]}`}
	turn := guardTurn(t, stub, agent)
	who := requesterIdentity{Authorized: true, AuthorizedAs: "Craig Coffee", AuthorizedVia: guardAuthAuthenticated}
	if _, err := turn.app.runWarden(turn.ctx, agent, guardHookPreInput, "what did you call me?", who); err != nil {
		t.Fatalf("runWarden: %v", err)
	}
	seen := stub.seen()
	if !strings.Contains(seen, "REQUESTER'S OWN incoming message") {
		t.Errorf("pre_input does not say whose words these are:\n%s", seen)
	}
	if strings.Contains(seen, "AGENT'S OWN candidate") {
		t.Errorf("pre_input was told the requester's own message is the agent's output:\n%s", seen)
	}
}

// A PROVENANCE clause is what the check cannot answer, so the editor says so
// before it ships. Narrow on purpose: naming the person resolves fine most of
// the time, because who is asking sits in the trusted block. What does not
// resolve is "only if it is directly from him" — the candidate is the agent's
// own draft and is never the requester speaking, so the judge hunts for an
// attribution that cannot be there.
//
// The live case reached "comply" three times, reversed each time, and settled
// on violate for want of proof. The rest of that exception was working.
//
// Asserted on the SOURCE because the detector lives in the editor's script and
// there is no JS runtime here. The phrase list is the point: if it stops
// covering the clause that cost an evening, this should fail.
func TestTheEditorWarnsAboutAProvenanceClause(t *testing.T) {
	src, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	page := string(src)
	if !strings.Contains(page, "function gExcAsksForProvenance(") {
		t.Fatal("the provenance check is gone")
	}
	if !strings.Contains(page, "'directly from'") {
		t.Error("the detector no longer covers the exact clause this exists for")
	}
	// It must NOT fire on naming somebody, which works.
	for _, tooBroad := range []string{"'bypass'", "'is allowed to'"} {
		if strings.Contains(page, "              "+tooBroad+",") {
			t.Errorf("the detector warns on %s, which resolves fine and would be a nag", tooBroad)
		}
	}
	if !strings.Contains(page, "Authorized people") {
		t.Error("the warning does not offer the deterministic alternative")
	}
}

// TestTheEchoWarningFiresOnlyOnAnEmptyCarveOut pins the narrowing that cost a
// second evening. The first version warned whenever the condition shared a word
// with its rule, which is every carve-out that names the rule's subject, so the
// one warning the editor has fired on correct input. What it must catch is a
// condition that adds NOTHING of its own: under a rule about the same subject
// that is not a carve-out, it is a repeal.
func TestTheEchoWarningFiresOnlyOnAnEmptyCarveOut(t *testing.T) {
	src, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	page := string(src)
	// The subset test: a word of the exception's own is what silences it.
	if !strings.Contains(page, "if (ruleWords.indexOf(words[k]) < 0) { adds = true; break; }") {
		t.Error("the detector no longer asks whether the condition adds words of its own")
	}
	if !strings.Contains(page, "if (!adds) { return true; }") {
		t.Error("the detector does not warn on the condition that adds nothing")
	}
	// The old any-word-shared form must not come back.
	if strings.Contains(page, "if (ruleWords.indexOf(words[k]) >= 0) { return true; }") {
		t.Error("the detector is back to warning on any shared word, which fires on correct carve-outs")
	}
}
