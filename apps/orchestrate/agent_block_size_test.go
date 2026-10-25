package orchestrate

// The Available agents block's size is logged per agent when it changes, with
// what a by-category collapse would act on.

import (
	"strings"
	"testing"
)

func TestTheAgentsBlockSizeIsLoggedWhenItChanges(t *testing.T) {
	id := "a-size-test"
	t.Cleanup(func() { agentBlockSizes.Delete(id) })
	entries := []agentBlockEntry{
		{name: "Stocks", category: "Markets", bytes: 300},
		{name: "Crypto", category: "markets ", bytes: 200},
		{name: "Deals", category: "Shopping", bytes: 150},
		{name: "Loner", bytes: 50},
	}
	line, changed := agentBlockSizeLine(id, 1800, entries)
	if !changed {
		t.Fatal("the first measurement was not written")
	}
	for _, want := range []string{
		"agent=a-size-test", "2500 bytes (~625 tokens)", "1800 fixed + 4 agents in 700 bytes",
		// Markets has two (case aside); Shopping's one is not a group.
		"2 of them share a category (500 bytes)",
		"largest: Stocks 300, Crypto 200, Deals 150, Loner 50",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q:\n%s", want, line)
		}
	}
	if _, again := agentBlockSizeLine(id, 1800, entries); again {
		t.Error("the same size was written twice")
	}
	if _, moved := agentBlockSizeLine(id, 1800, entries[:3]); !moved {
		t.Error("a changed size was not written")
	}
}
