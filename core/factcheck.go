package core

import (
	"regexp"
	"strings"
)

// Fact represents an extracted fact from text. Raw is the form as it appears
// in the source; Normalized is the comparable form used for verification.
type Fact struct {
	Raw        string
	Normalized string
	Kind       string // "number", "quote", etc.
}

// FactExtractor pulls facts of a specific kind from text.
type FactExtractor func(text string) []Fact

// factExtractors is the ordered list of registered extractors.
var factExtractors = []FactExtractor{
	extractNumbers,
}

// Number-with-unit pattern. Matches:
//
//	$2.52 trillion
//	$538 billion
//	90%
//	$4041.48 million
//	$109.1 billion
//	$3.497.26 trillion (malformed — still extracts what looks like digits)
//
// Capture groups:
//
//	[1] = digits (with optional commas and decimals)
//	[2] = unit (trillion/billion/million/thousand/percent) — may be empty
//	[3] = percent-form digits (for the "90%" pattern)
var numberRE = regexp.MustCompile(`\$?\s*([0-9][0-9,]*(?:\.[0-9]+)+|\d{2,})\s*(trillion|billion|million|thousand|percent)?|(\d+(?:\.\d+)?)\s*%`)

// unitSuffix returns the single-letter suffix for a unit word, or empty
// string if the unit is unrecognized. Used to build unit-aware normalized
// tokens so "$90 million" and "90%" don't substring-match each other.
func unitSuffix(unit string) string {
	switch strings.ToLower(unit) {
	case "trillion":
		return "T"
	case "billion":
		return "B"
	case "million":
		return "M"
	case "thousand":
		return "K"
	case "percent":
		return "%"
	}
	return ""
}

// normalizeNumber produces a canonical "digits+unit" token from a digit
// string and a unit word. Strips commas from digits and maps the unit to
// a single letter. Returns empty string if the result would be too
// short or ambiguous to verify.
func normalizeNumber(digits, unit string) string {
	digits = strings.ReplaceAll(digits, ",", "")
	suffix := unitSuffix(unit)
	if suffix == "" {
		// No unit means we can't reliably distinguish "210" (bias
		// detection millions) from "210" (page count, year fragment,
		// arbitrary sequence). Skip unitless numbers entirely — they
		// produce too many false positives.
		return ""
	}
	return digits + suffix
}

func extractNumbers(text string) []Fact {
	var facts []Fact
	for _, m := range numberRE.FindAllStringSubmatch(text, -1) {
		var digits, unit string
		switch {
		case m[1] != "":
			digits = m[1]
			unit = m[2]
		case m[3] != "":
			digits = m[3]
			unit = "percent"
		default:
			continue
		}
		normalized := normalizeNumber(digits, unit)
		if normalized == "" {
			continue
		}
		facts = append(facts, Fact{
			Raw:        strings.TrimSpace(m[0]),
			Normalized: normalized,
			Kind:       "number",
		})
	}
	return facts
}

// RejectedFact is a fact from the output that could not be verified
// against any parent source -- a candidate fabrication.
type RejectedFact struct {
	Fact
}

// VerifyFacts extracts facts from the output and checks each against
// the concatenated parent texts. Returns the list of facts that could
// not be verified.
//
// Verification is unit-aware: "$90 million" is normalized to "90M" and
// compared against a set of same-normalized tokens extracted from the
// parent text. "90%" normalizes to "90%" and does not match "90M", so
// the cross-unit false positive that plagued the substring approach is
// gone.
//
// Does NOT account for unit conversion (e.g., $1.5T vs $1500B) or
// rounding (e.g., "$200 billion" vs "$202 billion"). These produce
// false positives on legitimate paraphrases.
//
func VerifyFacts(output string, parentTexts []string) []RejectedFact {
	// Build a set of normalized fact tokens from the parent text by
	// running each extractor over the joined haystack. This replaces
	// the old substring-match approach with exact-token set membership.
	haystack := strings.Join(parentTexts, "\n")
	parentFacts := make(map[string]bool)
	for _, extractor := range factExtractors {
		for _, f := range extractor(haystack) {
			parentFacts[f.Kind+":"+f.Normalized] = true
		}
	}

	var rejected []RejectedFact
	seen := make(map[string]bool)

	for _, extractor := range factExtractors {
		for _, f := range extractor(output) {
			key := f.Kind + ":" + f.Normalized
			if seen[key] {
				continue
			}
			seen[key] = true

			if !parentFacts[key] {
				rejected = append(rejected, RejectedFact{Fact: f})
			}
		}
	}
	return rejected
}
