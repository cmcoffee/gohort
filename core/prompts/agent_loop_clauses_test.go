package prompts

import (
	"strings"
	"testing"
)

// coreLoopClauses pairs each registered key with the accessor the assembler
// calls. A clause added to one and not the other is exactly the drift the
// registration exists to prevent, so the table is the thing under test.
var coreLoopClauses = map[string]func() string{
	AnsweringRoundsKey:   AnsweringRoundsClause,
	GroundingContractKey: GroundingContractClause,
	CapabilityFirstKey:   CapabilityFirstClause,
	GroundingKey:         GroundingClause,
	ActionsKey:           ActionsClause,
	DisagreeingKey:       DisagreeingClause,
	NumbersKey:           NumbersClause,
	NoFalsePrecisionKey:  NoFalsePrecisionClause,
	SecretsKey:           SecretsClause,
	InternalMarkersKey:   InternalMarkersClause,
	VolatileFactsKey:     func() string { return VolatileFactsClause(false) },
	RoundBudgetKey:       func() string { return RoundBudgetClause(25) },
}

func registeredBlock(t *testing.T, key string) PromptBlock {
	t.Helper()
	for _, b := range AllPromptBlocks() {
		if b.Key == key {
			return b
		}
	}
	t.Fatalf("block %q is not registered: the Prompts page would omit it silently", key)
	return PromptBlock{}
}

// The page must be able to say what a clause is and when it applies. A block
// with no Gate is the state that made these hard to reason about in the first
// place: present on some turns, absent on others, with nothing saying which.
func TestCoreLoopClausesAreRegisteredWithTheirGate(t *testing.T) {
	for key := range coreLoopClauses {
		b := registeredBlock(t, key)
		if b.Title == "" || b.Category == "" || b.Gate == "" || b.Text == "" {
			t.Errorf("%s: incomplete registration %+v", key, b)
		}
		if !b.Builtin {
			t.Errorf("%s: registered from code, so it must be builtin", key)
		}
	}
}

// One string, not two. The registered text is what the assembler appends, so a
// person reading the page is reading the prompt rather than a copy of it.
func TestRegisteredTextIsWhatTheAssemblerAppends(t *testing.T) {
	withStore(t)
	for key, clause := range coreLoopClauses {
		b := registeredBlock(t, key)
		got := clause()
		switch key {
		case VolatileFactsKey, RoundBudgetKey:
			// Templated: the registered text carries the placeholder, the
			// accessor hands back the expansion. Compare the fixed halves.
			ph := "{lookup}"
			if key == RoundBudgetKey {
				ph = "{rounds}"
			}
			if !strings.Contains(b.Text, ph) {
				t.Errorf("%s: registered text should show %s, the shape actually sent", key, ph)
			}
			if strings.Contains(got, ph) {
				t.Errorf("%s: %s survived into the injected clause", key, ph)
			}
			head, tail := strings.Split(b.Text, ph)[0], strings.Split(b.Text, ph)[1]
			if !strings.HasPrefix(got, head) || !strings.HasSuffix(got, tail) {
				t.Errorf("%s: expansion does not match the registered template", key)
			}
		default:
			if got != b.Text {
				t.Errorf("%s: accessor and registry disagree\n accessor: %.80s\n registry: %.80s", key, got, b.Text)
			}
		}
	}
}

// Switching a rule off has to stop the append, not just grey out a row. The
// assembler treats "" as nothing to add, which is the whole contract.
func TestDisabledClauseAppendsNothing(t *testing.T) {
	withStore(t)
	for key, clause := range coreLoopClauses {
		SetPromptBlockEnabled(key, false)
		if got := clause(); got != "" {
			t.Errorf("%s: disabled block still injected %.60s", key, got)
		}
		SetPromptBlockEnabled(key, true)
		if clause() == "" {
			t.Errorf("%s: re-enabling did not restore the clause", key)
		}
	}
}

// An operator edit must reach the agent. A registry that only displayed text
// would pass every other test here and change nothing about the prompt.
func TestOverrideReachesTheAssembler(t *testing.T) {
	withStore(t)
	SetPromptOverride(ActionsKey, "[Actions: say only what you did.]")
	if got := ActionsClause(); got != "[Actions: say only what you did.]" {
		t.Errorf("override ignored, got %.60s", got)
	}
	ClearPromptOverride(ActionsKey)
	if ActionsClause() != registeredBlock(t, ActionsKey).Text {
		t.Error("clearing the override did not restore the shipped text")
	}
}

// Naming web_search to an agent that has no web tool tells it to call something
// another layer stripped, so the branch is chosen by the catalogue, not assumed.
func TestVolatileFactsNamesTheLookupTheAgentHas(t *testing.T) {
	withStore(t)
	if !strings.Contains(VolatileFactsClause(true), "call web_search or fetch_url FIRST") {
		t.Error("web-tool agent should be pointed at its search tools")
	}
	if strings.Contains(VolatileFactsClause(false), "call web_search or fetch_url FIRST") {
		t.Error("offline agent told to call a tool it does not have")
	}
	if !strings.Contains(VolatileFactsClause(false), "whatever search or fetch tool you have") {
		t.Error("offline agent lost the use-what-you-have branch")
	}
}

// The budget in the prompt is the budget of the turn. A stale or missing number
// is worse than no clause: it invites the model to pace against a fiction.
func TestRoundBudgetCarriesTheTurnsNumber(t *testing.T) {
	withStore(t)
	if !strings.Contains(RoundBudgetClause(12), "up to 12 tool-execution rounds") {
		t.Errorf("round budget did not carry its number: %.80s", RoundBudgetClause(12))
	}
}

// An edit that removes the placeholder is refused rather than shipped. The
// Prompts page can rewrite every block with an LLM in one pass, and a rewrite
// that tidies {rounds} away leaves the model told it has "up to
// tool-execution rounds" in fluent prose nothing downstream would flag.
func TestEditThatDropsThePlaceholderKeepsTheShippedWording(t *testing.T) {
	withStore(t)
	SetPromptOverride(RoundBudgetKey, "[Round budget: pace yourself.]")
	if got := RoundBudgetClause(9); !strings.Contains(got, "up to 9 tool-execution rounds") {
		t.Errorf("placeholder-less edit shipped: %.80s", got)
	}
	SetPromptOverride(VolatileFactsKey, "[Volatile facts: prices change.]")
	if got := VolatileFactsClause(true); !strings.Contains(got, "call web_search or fetch_url FIRST") {
		t.Errorf("placeholder-less edit shipped: %.80s", got)
	}
}

// An edit that KEEPS the placeholder is an operator's to make, and holds.
func TestEditThatKeepsThePlaceholderIsHonoured(t *testing.T) {
	withStore(t)
	SetPromptOverride(RoundBudgetKey, "[Round budget: {rounds} rounds, no more.]")
	if got := RoundBudgetClause(4); got != "[Round budget: 4 rounds, no more.]" {
		t.Errorf("edit not honoured, got %q", got)
	}
}

// Keys are addresses: two blocks answering to one key means an override or an
// off switch lands on whichever the page happened to list.
func TestClauseKeysAreUnique(t *testing.T) {
	seen := map[string]int{}
	for _, b := range AllPromptBlocks() {
		seen[b.Key]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("key %q registered %d times", key, n)
		}
	}
}
