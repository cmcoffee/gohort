package orchestrate

import (
	"strconv"
	"strings"
)

// declineLeakWords are phrases a decline must never contain. A decline exists
// to withhold WHY, so anything naming the mechanism, the rule, or the fact
// that a check ran hands a prober the bisection signal the guardrail is there
// to deny. Applied to model-written lines AND to owner-typed ones — the
// failure mode is the same either way.
// STEMS, not whole words: "rephrase" does not match "rephrasing", which is
// exactly how a leaky line slipped the first version of this filter.
//
// TWO TIERS, because one list could not tell a leak from a topic. The original
// single list held "system", "check", "verif", "configur", "permission" — all
// of which name the MECHANISM in "I can't tell you what the filter checked"
// and name the SUBJECT in "I can't get into the billing system for you".
// Measured against twenty ordinary refusals, seven were thrown out, and the
// seven were exactly the ones that said something specific. Every discard falls
// back to a canned line, so the filter was quietly converting useful refusals
// into the same generic sentence — the more the refusal was about anything, the
// likelier it was replaced.
//
// Tier one leaks on its own: nothing in an ordinary refusal says "guardrail" or
// "not allowed" unless the mechanism is being described. Tier two only leaks
// when the word did NOT come from the person asking. If they said "system", a
// refusal that says "system" is repeating them, not disclosing anything.
var declineLeakWords = []string{
	"guardrail", "not allowed", "not permitted", "forbid", "prohibit",
	"violat", "against my", "my rules",
	"content filter", "safety filter", "flagged",
	"rephras", "reword", "try again", "ask again", "differently",
}

// declineTopicalLeakWords leak only when the asker didn't raise them first.
// "complian" and the bare "rule" stem live down here rather than above because
// they are subject matter as often as mechanism — a deployment whose actual
// work is compliance reporting or account rules would otherwise be unable to
// decline in the vocabulary of its own domain.
var declineTopicalLeakWords = []string{
	"rule", "polic", "block", "restrict", "filter", "system",
	"instruct", "verif", "check", "permission", "configur", "complian",
}

// declineLeaks reports whether a candidate decline gives away why it fired,
// judged with no knowledge of what was asked. This is the AUTHORING-time gate
// (owner-typed and suggested lines), where there is no request to compare
// against and the lines have to be safe for every future block, so both tiers
// apply.
func declineLeaks(line string) bool { return declineLeaksAgainst(line, "") }

// declineLeaksAgainst is the BLOCK-time gate: same tier-one words, but a
// tier-two word is allowed through when it appears in the request being
// declined. Echoing the asker's own noun discloses nothing — they already know
// they said it — while a mechanism word they never used is the bisection signal
// a decline exists to withhold.
func declineLeaksAgainst(line, request string) bool {
	low := strings.ToLower(line)
	for _, w := range declineLeakWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	asked := strings.ToLower(request)
	for _, w := range declineTopicalLeakWords {
		if strings.Contains(low, w) && !strings.Contains(asked, w) {
			return true
		}
	}
	return false
}

// sanitizeDeclines trims, drops blanks and duplicates, drops any line that
// leaks WHY it fired, and caps the set. A rejected line is simply not stored:
// an over-informative decline is worse than the neutral built-in it replaces,
// so silently keeping the safe subset beats saving the lot.
func sanitizeDeclines(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		line := strings.TrimSpace(raw)
		if line == "" || seen[strings.ToLower(line)] || declineLeaks(line) {
			continue
		}
		seen[strings.ToLower(line)] = true
		out = append(out, line)
		if len(out) >= maxDeclines {
			break
		}
	}
	return out
}

const maxDeclines = 12

// lenOrNil reports a submitted list's length, or -1 when the key was absent —
// so the save log distinguishes "sent none" from "did not mention them", which
// is the difference between a user clearing a list and a stale page that has
// never heard of it.
func lenOrNil[T any](p *[]T) int {
	if p == nil {
		return -1
	}
	return len(*p)
}

// maxGuardrailExceptions caps the exception list. Every entry is a carve-out in
// something the owner wrote to be absolute, so the list has to stay short
// enough to read in one sitting.
const maxGuardrailExceptions = 16

// sanitizeGuardrailExceptions normalizes a submitted exception list: slugified
// names, trimmed text, no duplicates, capped.
//
// A blank name is DERIVED from the condition rather than dropped. The first
// version dropped it, which produced the worst possible result: the owner typed
// a condition, saved, saw no error, reopened, and found it gone — the feature
// reading as broken when it was doing exactly what it was told. Nothing that
// carries a real condition may fail to store; a name is a handle this code can
// invent, so it invents one.
//
// A blank CONDITION is still dropped, because there is nothing to invent from.
// An empty "Except:" line under a rule reads to the warden as a carve-out with
// no condition on it, which is indistinguishable from no rule at all.
func sanitizeGuardrailExceptions(in []GuardrailException) []GuardrailException {
	var out []GuardrailException
	seen := map[string]bool{}
	for _, raw := range in {
		text := strings.TrimSpace(raw.Text)
		if text == "" {
			continue
		}
		name := slugifyExceptionName(raw.Name)
		if name == "" {
			name = deriveExceptionName(text)
		}
		// A collision after slugging would silently merge two different
		// conditions under one handle, so suffix instead of dropping.
		base, n := name, 2
		for seen[name] {
			name = base + "-" + strconv.Itoa(n)
			n++
		}
		seen[name] = true
		out = append(out, GuardrailException{Name: name, Text: text, Kind: normalizeExceptionKind(raw.Kind)})
		if len(out) >= maxGuardrailExceptions {
			break
		}
	}
	return out
}

// deriveExceptionName invents a linkable handle from a condition's opening
// words, for an owner who wrote the condition and left the name blank.
func deriveExceptionName(text string) string {
	words := strings.Fields(text)
	if len(words) > 4 {
		words = words[:4]
	}
	if name := slugifyExceptionName(strings.Join(words, " ")); name != "" {
		return name
	}
	return "exception"
}

// slugifyExceptionName folds an owner's label into the character set a "@name"
// link can carry. Done at SAVE time, not at read time, so what is stored is
// what links: a name normalized on the way in can never disagree with the
// marker the rule was written with.
func slugifyExceptionName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '\t':
			// Collapse runs, and never lead with a separator.
			if cur := b.String(); cur != "" && !strings.HasSuffix(cur, "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// maxAuthorizedIdentities caps the roster. It is a master key for every rule
// marked "@", so a list long enough to lose track of is a list that stops being
// reviewed — and an unreviewed exemption roster is the whole failure mode this
// feature could have.
const maxAuthorizedIdentities = 24

// sanitizeAuthorizedIdentities trims, de-blanks and de-duplicates a submitted
// roster. It does NOT judge an entry's shape.
//
// It used to reject anything containing a space, on the theory that a bare
// display name ("Dana Whitfield") can never match and storing it would read as
// an authorization the owner believes they granted. Two things were wrong with
// that. It was factually incorrect for the commonest entry of all — SameHandle
// compares through the bridge's normalizeIdentity, which strips spaces and
// punctuation, so "+1 555 010 9999" matches the wire form perfectly and was
// being thrown away. And the rejection was SILENT: the owner typed a person,
// saved, saw no error, and found the roster empty, which is exactly the report
// that led here (authorized=0/1 kept in the save log).
//
// A stored entry that never matches is inert and VISIBLE — the owner can see it
// sitting in the list and work out that it is not doing anything. An entry the
// server ate is neither. Between those, keep it.
func sanitizeAuthorizedIdentities(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		entry := strings.TrimSpace(raw)
		if entry == "" || seen[strings.ToLower(entry)] {
			continue
		}
		seen[strings.ToLower(entry)] = true
		out = append(out, entry)
		if len(out) >= maxAuthorizedIdentities {
			break
		}
	}
	return out
}
