package orchestrate

import (
	"sort"
	"strings"
	"unicode"
)

// Matching a request to a shape, so agent creation can open with a DRAFT
// rather than an interview.
//
// The wizard asks its questions up front: type, personality, purpose, name.
// Most of them a person cannot answer well before they have seen an agent,
// which is why the answer to "what should its purpose be?" is so often a
// restatement of the question. Reacting to something concrete is easy;
// authoring from a blank page is not.
//
// A shape ships a whole agent, so the first proposal costs nothing: find the
// nearest shape to what the person typed, instantiate it, and show them what
// it is. The conversation after that is about the differences, which is a
// conversation anybody can have.
//
// Deterministic on purpose. This runs before any model call, on the first
// thing a user types, so it has to be instant, free, and the same every time;
// a shape that stops matching its own words should fail a test rather than
// drift with a model. When nothing matches, that is an answer too: the request
// is not an agent-from-a-shape, and Builder composes it from the recipe
// instead.

// shapeMatch is one scored candidate.
type shapeMatch struct {
	Shape  archetype
	Score  int
	Phrase string // the longest phrase that hit, for saying WHY it matched
}

// matchShapes ranks every shape against a request, best first, dropping the
// ones that did not match at all.
func matchShapes(request string) []shapeMatch {
	text := normalizeRequest(request)
	if text == "" {
		return nil
	}
	var out []shapeMatch
	for _, doc := range loadArchetypes() {
		score, phrase := scoreShape(doc, text)
		if score <= 0 {
			continue
		}
		out = append(out, shapeMatch{Shape: doc, Score: score, Phrase: phrase})
	}
	sortShapeMatches(out)
	return out
}

// sortShapeMatches ranks candidates: score first, then the shape that ships an
// agent, because a draft the user can look at beats a recipe somebody still
// has to build, and finally the slug so the order never depends on the order
// the directory happened to be read in.
func sortShapeMatches(out []shapeMatch) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		iShips, jShips := out[i].Shape.Record != nil, out[j].Shape.Record != nil
		if iShips != jShips {
			return iShips
		}
		return out[i].Shape.Slug < out[j].Shape.Slug
	})
}

// matchShape returns the single best shape for a request, or false when
// nothing fits well enough to propose.
func matchShape(request string) (shapeMatch, bool) {
	all := matchShapes(request)
	if len(all) == 0 {
		return shapeMatch{}, false
	}
	best := all[0]
	// A single short word is not a match worth opening with. "check" appears
	// in half the recipes; proposing a watcher because somebody typed it would
	// spend the user's first impression on a guess.
	if best.Score < shapeMatchFloor {
		return shapeMatch{}, false
	}
	// A near-tie is not a match either: two shapes fitting equally well means
	// the request has not said which, and asking beats guessing.
	if len(all) > 1 && best.Score-all[1].Score < shapeMatchMargin {
		return shapeMatch{}, false
	}
	return best, true
}

const (
	// shapeMatchFloor is the score below which nothing is proposed. One
	// two-word phrase clears it; a single incidental word does not.
	shapeMatchFloor = 2

	// shapeMatchMargin is how far ahead the winner must be. Equal scores mean
	// the request fits two shapes, and the dialog should ask rather than pick.
	shapeMatchMargin = 1
)

// scoreShape scores one shape against a normalized request, returning the
// score and the longest phrase that hit.
//
// Longer phrases count for more because they identify a shape: "cites sources"
// says research, while "agent" says nothing at all. Scoring by word count
// rather than by characters keeps a wordy phrase from beating a precise one.
func scoreShape(doc archetype, text string) (int, string) {
	score, longest := 0, ""
	for _, phrase := range doc.Match {
		p := normalizeRequest(phrase)
		if p == "" || !containsPhrase(text, p) {
			continue
		}
		score += wordCount(p)
		if len(p) > len(longest) {
			longest = phrase
		}
	}
	// The slug and its aliases are worth a point each: somebody who types
	// "research" or "kb" has named the shape, even without a phrase.
	for _, word := range append([]string{strings.ReplaceAll(doc.Slug, "_", " ")}, doc.Aliases...) {
		w := normalizeRequest(strings.ReplaceAll(word, "_", " "))
		if w != "" && containsPhrase(text, w) {
			score += wordCount(w)
			if longest == "" {
				longest = word
			}
		}
	}
	return score, longest
}

// containsPhrase reports whether text contains phrase on word boundaries, so
// "kb" does not match "webkbid" and "research" does not match "researcher"
// unless that word was listed too.
func containsPhrase(text, phrase string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], phrase)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(phrase)
		beforeOK := start == 0 || text[start-1] == ' '
		afterOK := end == len(text) || text[end] == ' '
		if beforeOK && afterOK {
			return true
		}
		i = start + 1
		if i >= len(text) {
			return false
		}
	}
}

// normalizeRequest lowercases, reduces everything that is not a letter, digit
// or apostrophe to a single space so punctuation and line breaks in a pasted
// request cannot hide a phrase, and folds every standalone number to "n".
//
// The number fold is what lets one phrase cover a cadence: "every n minutes"
// then matches "every 5 minutes", "every 30 minutes" and "every 2 hours"
// alike. A specific number never identifies a shape, so nothing is lost.
func normalizeRequest(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	words := strings.Fields(b.String())
	for i, w := range words {
		if isNumber(w) {
			words[i] = "n"
		}
	}
	return strings.Join(words, " ")
}

// isNumber reports a word that is all digits, which is what the fold replaces.
func isNumber(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return w != ""
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}
