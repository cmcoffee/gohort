package orchestrate

import (
	"regexp"
	"strings"
	"testing"
)

// The requests people actually type. Each is a sentence somebody would write
// into a "what do you want it to do?" box, not a category name, because
// matching category names is the easy half and never the half that fails.
func TestMatchShapeOnRealRequests(t *testing.T) {
	for _, tc := range []struct{ request, want string }{
		{"I want something that watches our status page and tells me when it goes down", "scheduled_watcher"},
		{"check the queue every 5 minutes and report the depth", "scheduled_watcher"},
		{"let me know when the PR is merged", "scheduled_watcher"},
		{"an agent that answers questions from our handbook", "knowledge_base"},
		{"a support bot grounded in our documentation", "knowledge_base"},
		{"something that only answers from what I upload", "knowledge_base"},
		{"something that looks things up and cites sources", "research"},
		{"a research agent for tariff news", "research"},
		{"figure out why the build keeps failing on that repo", "investigator"},
		{"someone to talk to about my day", "conversational"},
		{"a general assistant that can also do things", "conversational"},
	} {
		got, ok := matchShape(tc.request)
		if !ok {
			t.Errorf("no shape proposed for %q; the dialog would have to interview instead", tc.request)
			continue
		}
		if got.Shape.Slug != tc.want {
			t.Errorf("%q proposed %q (via %q), want %q", tc.request, got.Shape.Slug, got.Phrase, tc.want)
		}
	}
}

// Proposing the wrong thing costs more than proposing nothing: the first
// screen is what tells a new user whether this understood them.
func TestMatchShapeDeclinesRatherThanGuesses(t *testing.T) {
	for _, request := range []string{
		"build me an app for tracking invoices",
		"an agent",
		"help",
		"",
		"   ",
		"something for work",
	} {
		if got, ok := matchShape(request); ok {
			t.Errorf("%q proposed %q via %q; it should have asked", request, got.Shape.Slug, got.Phrase)
		}
	}
}

// A request that fits two shapes equally has not said which, and asking one
// question beats opening with a coin flip.
func TestANearTieAsksInsteadOfPicking(t *testing.T) {
	// Something to look at on a schedule, or something that looks things up:
	// the request contains both and settles neither.
	const both = "watch the page and look things up"
	all := matchShapes(both)
	if len(all) < 2 {
		t.Fatalf("expected two shapes to score, got %d", len(all))
	}
	if gap := all[0].Score - all[1].Score; gap >= shapeMatchMargin {
		t.Fatalf("this request is no longer a tie (%s=%d, %s=%d); pick another ambiguous one rather than deleting the check",
			all[0].Shape.Slug, all[0].Score, all[1].Shape.Slug, all[1].Score)
	}
	if got, ok := matchShape(both); ok {
		t.Errorf("a tie was resolved by picking %q rather than by asking", got.Shape.Slug)
	}

	// And a clear winner still wins: the margin must not turn every request
	// with two scoring shapes into a question.
	if got, ok := matchShape("a research agent that watches the page"); !ok || got.Shape.Slug != "research" {
		t.Errorf("a clear winner was refused: %q ok=%v", got.Shape.Slug, ok)
	}
}

// Every example a recipe gives for itself has to route back to it. This is
// what ties the phrases in the header to the "Build this when the user asks
// for..." sentence written for Builder, without demanding the two be worded
// identically: a person types differently from a recipe, which is the whole
// reason the phrases are declared separately.
func TestEachRecipesOwnExamplesMatchIt(t *testing.T) {
	quoted := regexp.MustCompile(`"([^"]{4,60})"`)
	for _, doc := range loadArchetypes() {
		para := buildWhenParagraph(doc.Body)
		if para == "" {
			t.Errorf("%s has no \"Build this when\" sentence, so nobody reading it knows when to reach for it", doc.Slug)
			continue
		}
		examples := quoted.FindAllStringSubmatch(para, -1)
		if len(examples) == 0 {
			t.Errorf("%s gives no example requests", doc.Slug)
			continue
		}
		for _, m := range examples {
			ranked := matchShapes(m[1])
			if len(ranked) == 0 {
				t.Errorf("%s: its own example %q matches no shape at all", doc.Slug, m[1])
				continue
			}
			if ranked[0].Shape.Slug != doc.Slug {
				t.Errorf("%s: its own example %q ranks %q first", doc.Slug, m[1], ranked[0].Shape.Slug)
			}
		}
	}
}

// buildWhenParagraph returns the paragraph beginning "Build this when".
func buildWhenParagraph(body string) string {
	for _, para := range strings.Split(body, "\n\n") {
		if strings.HasPrefix(strings.TrimSpace(para), "Build this when") {
			return para
		}
	}
	return ""
}

// A phrase that identifies two shapes identifies neither.
func TestNoTwoShapesClaimThePhrase(t *testing.T) {
	owner := map[string]string{}
	for _, doc := range loadArchetypes() {
		for _, p := range doc.Match {
			key := normalizeRequest(p)
			if prior, dup := owner[key]; dup {
				t.Errorf("%q is claimed by both %s and %s", p, prior, doc.Slug)
			}
			owner[key] = doc.Slug
		}
	}
}

// The normalizer decides what a phrase can see, so its edges are the matcher's
// edges.
func TestRequestNormalization(t *testing.T) {
	// A number is folded, so one cadence phrase covers every interval.
	if got := normalizeRequest("Every 30 Minutes!"); got != "every n minutes" {
		t.Errorf("normalized to %q", got)
	}
	// Punctuation and line breaks cannot hide a phrase in a pasted request.
	if got := normalizeRequest("watch the page,\nand tell me."); got != "watch the page and tell me" {
		t.Errorf("normalized to %q", got)
	}
	// Apostrophes survive, because "doesn't" is how people write.
	if got := normalizeRequest("admits when it doesn't know"); got != "admits when it doesn't know" {
		t.Errorf("normalized to %q", got)
	}
	// Phrases match on word boundaries: a shape is not proposed because its
	// name happened to appear inside another word.
	if containsPhrase("the webkbid service", "kb") {
		t.Error("a phrase matched inside a word")
	}
	if !containsPhrase("a kb assistant", "kb") {
		t.Error("a phrase failed to match a whole word")
	}
	if !containsPhrase("kb", "kb") {
		t.Error("a phrase failed to match the whole string")
	}
}

// A tie between a shape that ships an agent and one that does not goes to the
// one that can be shown, because a draft beats a recipe somebody still has to
// build.
func TestATiePrefersAShapeThatShipsAnAgent(t *testing.T) {
	ships, recipe := 0, 0
	for _, doc := range loadArchetypes() {
		if doc.Record != nil {
			ships++
		} else {
			recipe++
		}
	}
	if ships == 0 || recipe == 0 {
		t.Skip("the library no longer has both kinds")
	}
	a := archetype{Slug: "zzz", archetypeHeader: archetypeHeader{Record: &AgentRecord{ID: "x"}}}
	b := archetype{Slug: "aaa"}
	matches := []shapeMatch{{Shape: b, Score: 3}, {Shape: a, Score: 3}}
	sortShapeMatches(matches)
	if matches[0].Shape.Slug != "zzz" {
		t.Errorf("a tie preferred %q, the one with nothing to show", matches[0].Shape.Slug)
	}
}
