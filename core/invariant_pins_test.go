package core

import (
	"testing"

	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// Claims that HOLD in core with nothing pinning them. core is the layer every
// app inherits, so a broken invariant here reaches all of them at once.

// The guardrail hook labels core passes to GuardrailCheck and the labels an app
// accepts when resolving which hooks an agent enabled must be the same strings.
// The app aliases core's constants, so that half is compiler-enforced — but core
// itself hardcodes two of them as literals rather than using its own constants,
// and those literals are what this guards. GuardHookToolResult is deliberately
// absent from the app's valid set; that exclusion is by value, so a changed
// constant silently un-excludes it.
func TestGuardHookLabelsAreStable(t *testing.T) {
	for name, got := range map[string]string{
		"GuardHookPreInput":   GuardHookPreInput,
		"GuardHookPreAction":  GuardHookPreAction,
		"GuardHookPreOutput":  GuardHookPreOutput,
		"GuardHookPeriodic":   GuardHookPeriodic,
		"GuardHookToolResult": GuardHookToolResult,
	} {
		if got == "" {
			t.Errorf("%s is empty; the app's valid-hook set and core's own literals both match on value", name)
		}
	}
	// The two core sites that spell a hook out rather than using the constant.
	// If a constant's VALUE changes, those literals stop matching and the hook
	// silently stops firing — no compile error, no test, no log.
	if GuardHookPreOutput != "pre_output" {
		t.Errorf("GuardHookPreOutput = %q; core/agent_loop.go hardcodes \"pre_output\" in one place and would stop matching", GuardHookPreOutput)
	}
	if GuardHookPreAction != "pre_action" {
		t.Errorf("GuardHookPreAction = %q; core/agent_loop.go hardcodes \"pre_action\" for guardrailHalt and would stop matching", GuardHookPreAction)
	}
}

// Every operator knob's admin surface — form, GET, update validation, reset —
// is generated from the registry, so a knob is defined exactly once. The bounds
// the form renders are ints while the server validates in float; that is lossy
// the day a fractional Min is registered, and nothing else would say so.
func TestTunableBoundsSurviveTheFormsIntegerRounding(t *testing.T) {
	for _, s := range AllTunableSpecs() {
		if float64(int(s.Min)) != s.Min {
			t.Errorf("tunable %q has a fractional Min (%v); the admin form renders bounds as ints, so the form and the server would disagree about what is valid", s.Key, s.Min)
		}
		if float64(int(s.Max)) != s.Max {
			t.Errorf("tunable %q has a fractional Max (%v); same rounding problem", s.Key, s.Max)
		}
		if s.Min > s.Max {
			t.Errorf("tunable %q has Min %v above Max %v", s.Key, s.Min, s.Max)
		}
		if s.Default < s.Min || s.Default > s.Max {
			t.Errorf("tunable %q defaults to %v, outside its own bounds [%v, %v] — reset would write an invalid value", s.Key, s.Default, s.Min, s.Max)
		}
	}
}

// The admin deployment picker and the per-user /account picker must offer the
// same zone set. One builder, two call sites differing only in the blank-option
// label — this guards that the builder still produces a usable set at all, so a
// regression shows up here rather than as an empty dropdown on two pages.
func TestTimezoneOptionsComeFromOneBuilder(t *testing.T) {
	admin := TimezoneSelectOptions("Deployment default")
	account := TimezoneSelectOptions("Use deployment default")
	if len(admin) != len(account) {
		t.Fatalf("the two pickers offer %d and %d zones; they draw from one builder and must not differ", len(admin), len(account))
	}
	if len(admin) < 2 {
		t.Fatalf("the zone picker offers %d options; a blank plus at least one real zone is the minimum useful set", len(admin))
	}
	// Same zones in the same order — only the blank label may differ.
	for i := 1; i < len(admin); i++ {
		if admin[i] != account[i] {
			t.Fatalf("zone %d differs between the pickers: %q vs %q", i, admin[i], account[i])
		}
	}
}

// The source-hook cache key and the vector store's keyword tokenizer strip the
// same stopword set, from one exported list. The vector store additionally
// drops short terms, which the claim does not cover — what must hold is that
// there is one list.
func TestStopwordsAreOneList(t *testing.T) {
	if len(sourcehooks.Stopwords) == 0 {
		t.Fatal("the shared stopword list is empty; the cache key and the tokenizer would both stop stripping")
	}
	for _, w := range []string{"the", "a", "of"} {
		if !sourcehooks.Stopwords[w] {
			t.Errorf("%q is no longer a stopword; the cache key and the keyword tokenizer both read this list, so a query's cache identity just changed", w)
		}
	}
}

// The channel wake-rule default lives in Go and the client only carries it, so
// a "Reset to default" click writes the same text a new channel is seeded with.
func TestGatekeeperDefaultIsNonEmpty(t *testing.T) {
	if DefaultDMGatekeeperRule == "" {
		t.Fatal("the default wake rule is empty; seeding a channel and resetting one both write it, and neither would say so")
	}
}
