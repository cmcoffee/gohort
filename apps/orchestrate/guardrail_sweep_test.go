package orchestrate

// The one-time sweep that moves person exceptions onto the roster.
//
// It touches live agent records, so what it must never do matters more than
// what it does: lose an exemption, or duplicate one.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestSweepMovesAPersonExceptionOntoTheRoster(t *testing.T) {
	a := AgentRecord{
		Name: "X", Owner: "u",
		GuardrailExceptions: []GuardrailException{
			{Name: "craig", Text: "Craig Coffee", Kind: "person"},
			{Name: "confirmed", Text: "the user has already confirmed"},
		},
	}
	moved, kept, roster := splitPersonExceptions(a)

	if len(moved) != 1 || moved[0] != "Craig Coffee" {
		t.Fatalf("moved = %v", moved)
	}
	// The identity lands on the roster, which is the only thing that confers
	// authorization now.
	if len(roster) != 1 || roster[0] != "Craig Coffee" {
		t.Fatalf("roster = %v", roster)
	}
	// And the condition stays exactly where it was.
	if len(kept) != 1 || kept[0].Name != "confirmed" {
		t.Fatalf("kept = %+v", kept)
	}
}

// An owner who hit this confusion is likely to have listed the same person BOTH
// ways while trying to make it work. That must not produce two roster entries.
func TestSweepDoesNotDuplicateSomebodyAlreadyOnTheRoster(t *testing.T) {
	a := AgentRecord{
		Name: "X", Owner: "u",
		AuthorizedIdentities: []string{"Craig Coffee"},
		GuardrailExceptions: []GuardrailException{
			{Name: "craig", Text: "craig coffee", Kind: "person"},
		},
	}
	moved, kept, roster := splitPersonExceptions(a)
	if len(moved) != 1 {
		t.Fatalf("moved = %v", moved)
	}
	if len(roster) != 1 {
		t.Errorf("the same person landed on the roster twice: %v", roster)
	}
	if len(kept) != 0 {
		t.Errorf("the person exception should be gone from the list: %+v", kept)
	}
}

// Nothing to do is the common case once it has run, and running it again must
// be a no-op rather than a second migration.
func TestSweepIsANoOpOnAnAlreadySweptAgent(t *testing.T) {
	a := AgentRecord{
		Name: "X", Owner: "u",
		AuthorizedIdentities: []string{"Craig Coffee"},
		GuardrailExceptions:  []GuardrailException{{Name: "confirmed", Text: "already confirmed"}},
	}
	moved, kept, roster := splitPersonExceptions(a)
	if len(moved) != 0 {
		t.Errorf("a swept agent reported work to do: %v", moved)
	}
	if len(kept) != 1 || len(roster) != 1 {
		t.Errorf("a no-op sweep changed something: kept=%+v roster=%v", kept, roster)
	}
}

// A person entry carrying no identity matched nobody when it was written, so
// there is nothing to move and nothing worth keeping.
func TestSweepDropsAnEmptyPersonEntry(t *testing.T) {
	a := AgentRecord{
		Name: "X", Owner: "u",
		GuardrailExceptions: []GuardrailException{{Name: "nobody", Text: "  ", Kind: "person"}},
	}
	moved, kept, roster := splitPersonExceptions(a)
	if len(moved) != 0 || len(kept) != 0 || len(roster) != 0 {
		t.Errorf("moved=%v kept=%+v roster=%v", moved, kept, roster)
	}
}

// The sweep is registered where an admin can reach it. Asserted through the
// same registry the admin page reads, so a sweep nobody can run fails here
// rather than being discovered when somebody goes looking for the button.
func TestSweepIsRegisteredAsMaintenance(t *testing.T) {
	for _, m := range ListMaintenanceFuncs() {
		if m.Key == "guardrail_person_exceptions" {
			if !strings.Contains(strings.ToLower(m.Desc), "roster") {
				t.Errorf("the description should say where they go: %q", m.Desc)
			}
			return
		}
	}
	t.Fatal("the sweep is not registered, so nobody can run it")
}

// The sweep must not quietly remove the carve-out it is migrating.
//
// A rule written "@craig" parses to a named LINK; only a bare "@" sets
// ExceptAuthorized. Moving craig onto the roster without touching the rule
// leaves the link naming nothing, and a dangling link excepts nobody — so the
// person who WAS excepted silently becomes subject to the rule.
func TestSweepRepointsRulesThatLinkedAMovedPerson(t *testing.T) {
	rules := "@craig never mention salary\nnever delete records"
	got := rewriteMovedPersonLinks(rules, []string{"craig"}, nil)

	want := "@ never mention salary\nnever delete records"
	if got != want {
		t.Fatalf("rewrote to %q, want %q", got, want)
	}
	// And the rewritten rule actually excepts an authorized requester, which is
	// the property the text change exists for.
	r := parseGuardrailRule("@ never mention salary")
	if !ruleExemptsRequester(r, requesterIdentity{Authorized: true}) {
		t.Error("the rewritten rule does not except an authorized person")
	}
}

// A link switched OFF for one rule meant "this rule applies to them anyway".
// Turning that into a bare "@" would invert the owner's decision, so it is
// dropped instead.
func TestSweepDropsASwitchedOffLinkRatherThanWideningIt(t *testing.T) {
	got := rewriteMovedPersonLinks("@-craig never mention salary", []string{"craig"}, nil)
	if got != "never mention salary" {
		t.Fatalf("rewrote to %q", got)
	}
	if r := parseGuardrailRule(got); r.ExceptAuthorized {
		t.Error("a switched-off link became a whole-roster exception")
	}
}

// Only the moved name, and only as a whole marker.
func TestSweepLeavesOtherLinksAlone(t *testing.T) {
	// A surviving CONDITION keeps its link: it resolves to a real carve-out.
	got := rewriteMovedPersonLinks("@craig @confirmed never send money", []string{"craig"},
		[]GuardrailException{{Name: "confirmed", Text: "the user has already confirmed"}})
	if got != "@ @confirmed never send money" {
		t.Fatalf("rewrote to %q", got)
	}
	// A name that merely starts with the moved one is not the moved one.
	if got := rewriteMovedPersonLinks("@craigslist never post", []string{"craig"}, nil); got != "@craigslist never post" {
		t.Errorf("matched inside a longer name: %q", got)
	}
	// A name still used by a surviving condition is left alone entirely.
	if got := rewriteMovedPersonLinks("@craig never x", []string{"craig"},
		[]GuardrailException{{Name: "craig", Text: "the request is routine"}}); got != "@craig never x" {
		t.Errorf("repointed a link that still resolves: %q", got)
	}
	// Nothing to move, nothing to do.
	if got := rewriteMovedPersonLinks("@craig never x", nil, nil); got != "@craig never x" {
		t.Errorf("changed a rule with no moved people: %q", got)
	}
}
