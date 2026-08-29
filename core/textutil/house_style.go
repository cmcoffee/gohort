// House-style enforcement that is a pure function of the text.
//
// The companion to StripEmDashes, and it exists for the same reason. The
// system prompt carries a rule telling the model to stop using "classic" as
// filler; a worker-tier model broke that rule and the em-dash half of it in
// the same six-turn conversation. Asking a model not to emit a token is a
// probability nudge. This is a guarantee.
//
// The line drawn here matters: only the FILLER use goes. "Classic" is a real
// word with a literal sense, and a rewrite that flattened "a classic car" into
// "a car" would be a worse bug than the tic it fixes.
package textutil

import (
	"regexp"
	"strings"
)

// fillerClassicRe matches "classic" sitting between a determiner and the word
// it modifies, which is the shape the filler use always takes ("a classic
// mistake", "the classic example"). Anchoring on BOTH sides is what keeps the
// head-noun use safe: "that's a classic" and "a classic of the genre" have no
// following modifier, so they never match and are never touched.
//
// Only lowercase "classic" matches. Capitalised is either sentence-initial or
// part of a name (Coca-Cola Classic, Classic Mode), and neither is filler.
var fillerClassicRe = regexp.MustCompile(`\b([Aa]|[Tt]he|[Tt]his|[Tt]hat|[Tt]hese|[Tt]hose|[Yy]our|[Mm]y|[Oo]ur|[Ii]ts|[Tt]heir)\s+classic\s+([a-z][a-z-]*)`)

// headNounFollowers are words that, coming after "classic", prove it is the
// HEAD of the phrase rather than a modifier of something else: "a classic of
// the genre", "a classic that everyone knows". The regex cannot tell those from
// "a classic mistake" on shape alone, and removing the word there leaves "a of
// the genre". Function words are the tell, so they veto the rewrite.
var headNounFollowers = map[string]bool{
	"of": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"from": true, "by": true, "with": true, "about": true,
	"that": true, "which": true, "who": true, "when": true, "where": true,
	"and": true, "or": true, "but": true, "though": true, "because": true,
	"is": true, "was": true, "as": true, "if": true, "so": true,
}

// literalClassicNouns are the nouns where "classic" is doing real work rather
// than padding, so the phrase is left exactly as written. Deliberately short:
// a long list starts guessing, and a false SKIP only leaves a tic in place
// while a false STRIP changes what the sentence says.
var literalClassicNouns = map[string]bool{
	"car": true, "cars": true,
	"edition": true, "editions": true,
	"album": true, "albums": true,
	"film": true, "films": true,
	"movie": true, "movies": true,
	"novel": true, "novels": true,
	"guitar": true, "guitars": true,
	"rock": true, "literature": true, "arcade": true, "era": true,
}

// StripFillerClassic removes "classic" where it is padding a noun phrase,
// leaving the phrase grammatical. Code is preserved: anything inside a fenced
// ``` block or an inline `code` span is left exactly as written, the same
// protection StripEmDashes applies. Fast no-op when the word is absent.
func StripFillerClassic(s string) string {
	if !strings.Contains(s, "classic") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, loc := range codeSpanRe.FindAllStringIndex(s, -1) {
		b.WriteString(dropFillerClassic(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]]) // code span verbatim
		last = loc[1]
	}
	b.WriteString(dropFillerClassic(s[last:]))
	return b.String()
}

// dropFillerClassic applies the rewrite to one non-code segment.
func dropFillerClassic(seg string) string {
	if !strings.Contains(seg, "classic") {
		return seg
	}
	return fillerClassicRe.ReplaceAllStringFunc(seg, func(m string) string {
		sub := fillerClassicRe.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		det, noun := sub[1], sub[2]
		lower := strings.ToLower(noun)
		if literalClassicNouns[lower] || headNounFollowers[lower] {
			return m
		}
		// Removing the word can strand the wrong article: "a classic error"
		// must not become "a error". The determiner has to agree with whatever
		// it now sits in front of.
		if strings.EqualFold(det, "a") && startsWithVowelSound(noun) {
			if det == "A" {
				det = "An"
			} else {
				det = "an"
			}
		}
		return det + " " + noun
	})
}

// startsWithVowelSound is the article test, by spelling. Good enough for the
// job: it decides between "a" and "an" on a word the sentence already
// contained, so the failure mode is a slightly awkward article, never a
// changed meaning.
func startsWithVowelSound(word string) bool {
	if word == "" {
		return false
	}
	switch word[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}
