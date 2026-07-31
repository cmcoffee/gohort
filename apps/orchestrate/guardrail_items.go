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
	"strconv"
	"strings"
)

// Item kinds.
const (
	guardrailKindPerson    = "person"
	guardrailKindCondition = "condition"
)

// guardrailItem is one resolved carve-out.
type guardrailItem struct {
	Name string
	Kind string
	Text string // the condition's wording, or the person's identity
	// Legacy is set on an item materialized from the old AuthorizedIdentities
	// roster rather than authored as an item. It behaves identically; the flag
	// exists so the editor can say where it came from instead of presenting a
	// migration as something the owner typed.
	Legacy bool
}

// guardrailItems returns the agent's carve-outs: the authored list, plus any
// legacy roster entry that has no item of its own yet.
//
// Merged on READ rather than migrated on write. A migration that rewrites the
// record needs every save path to be holding the new shape already, and this
// session has spent enough on fields that saved perfectly and read back empty.
// Reading both means the old field keeps working untouched and the new one wins
// wherever it exists.
func guardrailItems(agent AgentRecord) []guardrailItem {
	var out []guardrailItem
	seenName := map[string]bool{}
	seenText := map[string]bool{}
	for _, e := range agent.GuardrailExceptions {
		name := slugifyExceptionName(e.Name)
		text := strings.TrimSpace(e.Text)
		if name == "" || text == "" || seenName[name] {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(e.Kind))
		if kind != guardrailKindPerson {
			kind = guardrailKindCondition
		}
		seenName[name] = true
		if kind == guardrailKindPerson {
			seenText[strings.ToLower(text)] = true
		}
		out = append(out, guardrailItem{Name: name, Kind: kind, Text: text})
	}
	// Legacy roster entries that were never turned into items.
	for _, id := range agent.AuthorizedIdentities {
		id = strings.TrimSpace(id)
		if id == "" || seenText[strings.ToLower(id)] {
			continue
		}
		name := slugifyExceptionName(id)
		if name == "" {
			name = "person"
		}
		base, n := name, 2
		for seenName[name] {
			name = base + "-" + strconv.Itoa(n)
			n++
		}
		seenName[name] = true
		seenText[strings.ToLower(id)] = true
		out = append(out, guardrailItem{Name: name, Kind: guardrailKindPerson, Text: id, Legacy: true})
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
		if !ok || it.Kind != guardrailKindCondition {
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
	if r.ExceptAuthorized && req.Authorized {
		return true
	}
	if len(r.Links) == 0 || len(req.AuthorizedNames) == 0 {
		return false
	}
	matched := map[string]bool{}
	for _, n := range req.AuthorizedNames {
		matched[n] = true
	}
	for _, link := range r.Links {
		if !link.Off && matched[link.Name] {
			return true
		}
	}
	return false
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

// normalizeExceptionKind folds a submitted kind to one of the two values.
// Anything unrecognized becomes a condition — the judged kind, which is the
// weaker claim: a mistyped kind must not silently promote an item to something
// the framework treats as proof of identity.
func normalizeExceptionKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), guardrailKindPerson) {
		return guardrailKindPerson
	}
	return guardrailKindCondition
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
	if name := slugifyExceptionName(as); name != "" {
		// A stand-in that names nobody must NOT fall through to the owner, who
		// is excepted from everything — the test would answer "nothing blocks"
		// for a person who does not exist, which is the most misleading result
		// available. It resolves to a nobody instead: every rule in force.
		found := false
		for _, it := range guardrailItems(agent) {
			if it.Name == name && it.Kind == guardrailKindPerson {
				found = true
				break
			}
		}
		if !found {
			return requesterIdentity{Name: strings.TrimSpace(sender), Channel: "channel"}
		}
		for _, it := range guardrailItems(agent) {
			if it.Name != name || it.Kind != guardrailKindPerson {
				continue
			}
			// Every item this identity satisfies, not just the one named — the
			// same person can be listed twice (an account and a phone), and the
			// live path matches both.
			who := requesterIdentity{
				Authorized: true, AuthorizedAs: it.Text,
				AuthorizedVia: guardAuthAuthenticated, Channel: "channel",
			}
			for _, other := range guardrailItems(agent) {
				if other.Kind == guardrailKindPerson && strings.EqualFold(other.Text, it.Text) {
					who.AuthorizedNames = append(who.AuthorizedNames, other.Name)
				}
			}
			return who
		}
	}
	if s := strings.TrimSpace(sender); s != "" {
		// An outside contact: a self-reported name and nothing established.
		return requesterIdentity{Name: s, Channel: "channel"}
	}
	// The owner, who satisfies every person item — as they do live.
	who := requesterIdentity{Owner: true, Authorized: true, AuthorizedVia: guardAuthAuthenticated}
	for _, it := range guardrailItems(agent) {
		if it.Kind == guardrailKindPerson {
			who.AuthorizedNames = append(who.AuthorizedNames, it.Name)
		}
	}
	return who
}
