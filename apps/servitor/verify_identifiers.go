package servitor

import (
	"strings"
	"unicode"
)

// identifierStoplist is prose that looks like an identifier to the shape test
// below but never names anything on a system.
var identifierStoplist = map[string]bool{
	"e.g": true, "i.e": true, "etc": true, "vs": true, "a.m": true, "p.m": true,
}

// identifierTrim is the punctuation stripped from both ends of a token before
// it is judged: sentence and markdown decoration, never part of a name.
const identifierTrim = "()[]{}<>\"'`*.,;:!?|"

// unverifiedIdentifiers returns every identifier-shaped token in reply that
// does not appear character-for-character in findings. It is the deterministic
// front of the verification pass: the verifier model's only job is to catch
// identifiers the reply spells differently from the findings, so when this
// returns nothing the model has nothing to do and is not called.
//
// The shape test is deliberately broad — a token counts as an identifier if it
// carries a path or name separator, a digit, or a capital past its first
// letter — because the cost of a false positive is one model call the reply
// would have paid anyway, while a false negative would skip a real check.
//
// A word whose only capital is its first letter is ambiguous: "Nginx" is a
// name, "The" is a sentence. It counts when it sits mid-sentence AND the
// findings contain it in some other case — the one situation where the
// verifier could correct it, since a correction must quote the findings
// verbatim. Sentence-initial words are exempt; so is a name the findings do not
// mention at all, which the verifier could not fix either.
func unverifiedIdentifiers(reply, findings string) []string {
	lowerFindings := strings.ToLower(findings)
	seen := make(map[string]bool)
	var out []string
	for _, line := range strings.Split(reply, "\n") {
		boundary := true // a line start is a sentence start
		for _, raw := range strings.Fields(line) {
			tok := strings.Trim(raw, identifierTrim)
			atStart := boundary
			boundary = tok == "" || endsSentence(raw)
			if len(tok) < 2 || seen[tok] || identifierStoplist[strings.ToLower(tok)] {
				continue
			}
			candidate := looksLikeIdentifier(tok)
			if !candidate && !atStart && leadingCapitalOnly(tok) {
				candidate = strings.Contains(lowerFindings, strings.ToLower(tok))
			}
			if !candidate {
				continue
			}
			seen[tok] = true
			if !strings.Contains(findings, tok) {
				out = append(out, tok)
			}
		}
	}
	return out
}

// endsSentence reports whether a raw token closes a sentence or a label once
// its trailing markdown decoration is set aside: "done.", "**Summary:**".
func endsSentence(raw string) bool {
	raw = strings.TrimRight(raw, "*`\"')]}")
	return raw != "" && strings.ContainsRune(".!?:", rune(raw[len(raw)-1]))
}

// looksLikeIdentifier is the shape test described on unverifiedIdentifiers.
func looksLikeIdentifier(tok string) bool {
	alnum := false
	for i, r := range tok {
		switch {
		case unicode.IsDigit(r):
			return true
		case strings.ContainsRune("/_.-:@", r):
			// A separator only counts inside a name: "--" or "..." alone is
			// decoration, not an identifier.
			if alnum {
				return true
			}
		case unicode.IsUpper(r) && i > 0:
			return true
		case unicode.IsLetter(r):
			alnum = true
		}
	}
	return false
}

// leadingCapitalOnly reports a token shaped like "Nginx": one capital first
// letter, then only lowercase letters.
func leadingCapitalOnly(tok string) bool {
	for i, r := range tok {
		if i == 0 {
			if !unicode.IsUpper(r) {
				return false
			}
			continue
		}
		if !unicode.IsLower(r) {
			return false
		}
	}
	return true
}
