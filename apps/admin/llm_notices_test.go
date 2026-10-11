package admin

// Whatever is wrong with the lead is ONE dashboard card. A model that fails
// its handoff check goes on to fail its calls, and two cards with the same
// button read like two things to fix. The worker keeps its own card.

import (
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestLeadProblemsAreOneCard(t *testing.T) {
	handoffMu.Lock()
	prev := handoffLast
	handoffLast = map[string]handoffResult{
		LeadLLMTable: {model: "lead-model", first: "Tool call, then its result: refused."},
	}
	handoffMu.Unlock()
	t.Cleanup(func() {
		handoffMu.Lock()
		handoffLast = prev
		handoffMu.Unlock()
		SetLeadInitError("", "", nil)
	})
	SetLeadInitError("provider", "lead-model", errors.New("no credentials"))

	a := &AdminApp{}
	got := a.llmNotices()
	if len(got) != 1 {
		t.Fatalf("got %d cards, want one for the lead: %+v", len(got), got)
	}
	for _, want := range []string{"could not be initialized", "failed its handoff check"} {
		if !strings.Contains(got[0].Text, want) {
			t.Errorf("the lead card does not say %q: %s", want, got[0].Text)
		}
	}
	if !strings.HasPrefix(got[0].Text, "The configured lead model") {
		t.Errorf("the card should open with a capital: %s", got[0].Text)
	}

	handoffMu.Lock()
	handoffLast[LLMTable] = handoffResult{model: "worker-model", first: "Two calls in one turn: refused."}
	handoffMu.Unlock()
	if got = a.llmNotices(); len(got) != 2 || !strings.Contains(got[1].Text, "worker model (worker-model)") {
		t.Errorf("the worker's problem should be its own card: %+v", got)
	}

	SetLeadInitError("", "", nil)
	handoffMu.Lock()
	handoffLast = map[string]handoffResult{}
	handoffMu.Unlock()
	if got = a.llmNotices(); len(got) != 0 {
		t.Errorf("nothing wrong should be no card: %+v", got)
	}
}
