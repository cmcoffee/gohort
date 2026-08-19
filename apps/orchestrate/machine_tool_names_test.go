package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A catalog as the runtime would hold it: MCP proxies under their published
// (lowercased, server-prefixed) names, plus an ordinary built-in.
func namesCatalog() map[string]bool {
	known := map[string]bool{}
	for _, n := range []string{
		"web_search",
		"atlassian_search",
		"atlassian_getconfluencepage",
		"atlassian_createjiraissue",
		"confluence_search",
	} {
		known[n] = true
	}
	return known
}

func findingsFor(tools ...string) []string {
	return phaseToolFindings(MachineDef{
		Phases: []MachinePhase{{Name: "investigate", Tools: tools}},
	}, namesCatalog())
}

func TestPhaseToolNamesThatResolveAreNotReported(t *testing.T) {
	if got := findingsFor("web_search", "atlassian_search"); len(got) != 0 {
		t.Fatalf("correct names were flagged: %v", got)
	}
}

// The live failure: an author copies the server's own tool list, which is
// camelCase, and the catalog holds it lowercased.
func TestPhaseToolCaseMismatchSuggestsTheCatalogSpelling(t *testing.T) {
	got := findingsFor("atlassian_getConfluencePage", "web_search")
	if len(got) != 1 {
		t.Fatalf("want exactly the one misnamed tool reported, got %v", got)
	}
	if !strings.Contains(got[0], `did you mean "atlassian_getconfluencepage"?`) {
		t.Errorf("a recoverable name should carry its correction, got %q", got[0])
	}
}

// MCP spells the pair with a dot; the catalog cannot (a dot is outside the
// character class every model provider validates a tool name against).
func TestPhaseToolDotSeparatorSuggestsTheUnderscoreForm(t *testing.T) {
	got := findingsFor("atlassian.search")
	if len(got) == 0 || !strings.Contains(got[0], `did you mean "atlassian_search"?`) {
		t.Errorf("want the underscore form suggested, got %v", got)
	}
}

// The raw remote name, with no server prefix — recoverable only while it is
// unambiguous.
func TestPhaseToolRawRemoteNameSuggestsThePrefixedForm(t *testing.T) {
	got := findingsFor("getConfluencePage")
	if len(got) == 0 || !strings.Contains(got[0], `did you mean "atlassian_getconfluencepage"?`) {
		t.Errorf("want the server-prefixed form suggested, got %v", got)
	}
}

// Two servers offering the same verb is exactly when a guess would send
// somebody to the wrong system. Say nothing rather than something wrong.
func TestAmbiguousRawNameGetsNoSuggestion(t *testing.T) {
	got := findingsFor("search")
	if len(got) == 0 {
		t.Fatal("an unresolvable name should still be reported")
	}
	if strings.Contains(got[0], "did you mean") {
		t.Errorf("two servers answer to this name — a suggestion would be a coin toss: %q", got[0])
	}
	if !strings.Contains(got[0], "must match the catalog exactly") {
		t.Errorf("without a suggestion the finding has to teach the rule, got %q", got[0])
	}
}

// A list where nothing matched has a different consequence from a list with
// one bad entry: narrowCatalog refuses to resolve it to nothing, so the step
// runs with everything — the opposite of what the list was written for. The
// checklist has to say that, or the author fixes one name and wonders why the
// step's behaviour did not change.
func TestPhaseWhereNothingMatchesSaysTheStepRunsUnnarrowed(t *testing.T) {
	got := findingsFor("atlassian_getConfluencePage", "atlassian.createJiraIssue")
	if len(got) != 3 {
		t.Fatalf("want a finding per name plus the whole-list finding, got %v", got)
	}
	last := got[len(got)-1]
	if !strings.Contains(last, "FULL catalog") {
		t.Errorf("the whole-list consequence is missing: %q", last)
	}
}

// One good name is enough to make the narrowing real, so the whole-list
// finding must not fire alongside it.
func TestOneGoodNameSuppressesTheWholeListFinding(t *testing.T) {
	got := findingsFor("web_search", "atlassian_getConfluencePage")
	for _, f := range got {
		if strings.Contains(f, "FULL catalog") {
			t.Errorf("the step IS narrowed — reporting otherwise is wrong: %q", f)
		}
	}
}

// Findings start "step <name>:" because the editor turns that prefix into a
// link to the step's own section (findingStep in the page script).
func TestFindingsCarryTheStepPrefixTheEditorLinksOn(t *testing.T) {
	for _, f := range findingsFor("nope") {
		if !strings.HasPrefix(f, "step investigate:") {
			t.Errorf("finding %q cannot be linked to its step", f)
		}
	}
}

// A machine that narrows nothing pays for none of the catalog gathering.
func TestNoPhaseNamesToolsMeansNoCatalogWork(t *testing.T) {
	def := MachineDef{Phases: []MachinePhase{{Name: "investigate"}, {Name: "answer"}}}
	if known := knownAgentToolNames(nil, "someone@example.com", def); known != nil {
		t.Errorf("nothing to check should cost nothing, got %d name(s)", len(known))
	}
}
