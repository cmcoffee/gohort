package orchestrate

// The carve-out list: people and conditions, in one place, linked to rules
// individually.
//
// This replaced two parallel mechanisms. "Authorized people" was a single
// roster that every rule marked "@" was excepted for, all-or-nothing — so a
// rule could not be excepted for one person and not another. Named exceptions
// were a separate list with a separate picker. An owner wanting "not for Dana"
// had to reach for a different concept than "not when already confirmed", for
// no reason that survives being asked about.
//
// So: one list of named items, each either a PERSON or a CONDITION, and a rule
// links whichever ones apply to it. Who settles a link depends on its kind, and
// that difference is real rather than cosmetic:
//
//	person    — the FRAMEWORK matches it against the requester it established
//	            (authenticated account, or a handle the bridge verified). The
//	            rule is dropped before the warden is called. Nothing a requester
//	            writes can reach this.
//	condition — the warden reads it on an "Except:" line under the rule and
//	            decides whether it holds. Prose, judged.
//
// The old roster still reads: its entries surface as person items so a roster
// typed before any of this keeps working, and a plain "@" on a rule still means
// "any of them".

import (
	"strings"
)

// guardrailItem is one resolved carve-out: a CONDITION the check reads under
// every rule that links it.
//
// There used to be a second kind, a person, which the framework resolved itself
// against who was asking. It is gone, and the roster it duplicated
// (AgentRecord.AuthorizedIdentities) does that job alone now. Two mechanisms for
// "this person is exempt" meant two lists that could disagree, one name that
// could mean either of them, and — because a rule links by NAME — a link that
// silently reached whichever one was stored first.
type guardrailItem struct {
	Name string
	Text string // the condition's wording
}

// guardrailItems returns the agent's authored carve-outs.
//
// The authored list and NOTHING else. It used to merge legacy roster entries in
// on every read, which is what made a deleted exception come back: the item was
// removed, the roster entry behind it was not, and the next read rebuilt it.
// The roster is now only ever an identity list, and the one-time sweep in
// guardrail_sweep.go moved anything that was pulling double duty.
func guardrailItems(agent AgentRecord) []guardrailItem {
	var out []guardrailItem
	seenName := map[string]bool{}
	for _, e := range agent.GuardrailExceptions {
		name := slugifyExceptionName(e.Name)
		text := strings.TrimSpace(e.Text)
		if name == "" || text == "" || seenName[name] {
			continue
		}
		seenName[name] = true
		out = append(out, guardrailItem{Name: name, Text: text})
	}
	return out
}

// guardrailItemsByName indexes the list for link resolution.
func guardrailItemsByName(agent AgentRecord) map[string]guardrailItem {
	byName := map[string]guardrailItem{}
	for _, it := range guardrailItems(agent) {
		byName[it.Name] = it
	}
	return byName
}

// ruleConditionTexts returns the CONDITION wording for a rule's active links,
// in the order they were linked — what the warden reads under the rule.
//
// A link that is switched off, or names an item that no longer exists, or names
// a person (settled by the framework, not the warden) contributes nothing here.
// All three leave the rule at full strength, which is the direction every
// unresolvable thing in this file fails in.
func ruleConditionTexts(agent AgentRecord, r guardrailRule) []string {
	if len(r.Links) == 0 {
		return nil
	}
	byName := guardrailItemsByName(agent)
	var out []string
	seen := map[string]bool{}
	for _, link := range r.Links {
		if link.Off || seen[link.Name] {
			continue
		}
		it, ok := byName[link.Name]
		if !ok {
			continue
		}
		seen[link.Name] = true
		out = append(out, it.Text)
	}
	return out
}

// ruleExemptsRequester reports whether this rule is out of play for the person
// asking, on the strength of its PERSON links alone.
//
// Settled here, before the warden call, because it is a fact the framework
// already computed — requesterIdentity carries which items the requester
// matched, established from the dispatch path and the transport's attribution
// and reachable by nothing a requester sends.
//
// A bare "@" (the legacy whole-roster marker) means any person item at all.
func ruleExemptsRequester(r guardrailRule, req requesterIdentity) bool {
	return r.ExceptAuthorized && req.Authorized
}

// rulesInPlayFor drops the rules this requester is exempt from, leaving the set
// the warden is actually asked about. Byte-identical behaviour to before links
// existed when nothing is linked.
func rulesInPlayFor(rules []guardrailRule, req requesterIdentity) []guardrailRule {
	var out []guardrailRule
	for _, r := range rules {
		if ruleExemptsRequester(r, req) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// testRequester builds the identity a dry-run check should judge against.
//
// It mirrors chatTurn.requester() rather than hand-rolling a struct, which is
// the bug it replaces: the test used to set Owner:true and nothing else, so
// AuthorizedNames was empty and a person-linked rule was never skipped — the
// test reported a block that production would not produce. A test that can
// quietly disagree with enforcement is worse than no test.
//
// Choosing the identity is safe here and nowhere else: the caller is already
// the authenticated owner of the record, and the worst they can do is run a dry
// check against their own rules.
func testRequester(agent AgentRecord, as, sender string) requesterIdentity {
	if want := strings.TrimSpace(as); want != "" {
		// Matched against the ROSTER, exactly as the live path does — the same
		// whole-string compare, so a dry run cannot report an exemption that
		// production would not give. A first name that is not on the roster
		// resolves to nobody, which is the honest answer: every rule in force.
		for _, id := range authorizedIdentities(agent) {
			if strings.EqualFold(id, want) {
				return requesterIdentity{
					Authorized: true, AuthorizedAs: id,
					AuthorizedVia: guardAuthAuthenticated, Channel: "channel",
				}
			}
		}
		return requesterIdentity{Name: want, Channel: "channel"}
	}
	if s := strings.TrimSpace(sender); s != "" {
		// An outside contact: a self-reported name and nothing established.
		return requesterIdentity{Name: s, Channel: "channel"}
	}
	// The owner, authorized over their own agent — as they are live.
	return requesterIdentity{Owner: true, Authorized: true, AuthorizedVia: guardAuthAuthenticated}
}
