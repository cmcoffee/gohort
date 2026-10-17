package admin

// Whatever is wrong with the lead is ONE dashboard card. A model that cannot
// start goes on to fail its calls, and two cards with the same button read
// like two things to fix.

import (
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestLeadProblemsAreOneCard(t *testing.T) {
	t.Cleanup(func() { SetLeadInitError("", "", nil) })
	SetLeadInitError("provider", "lead-model", errors.New("no credentials"))

	a := &AdminApp{}
	got := a.llmNotices()
	if len(got) != 1 || !strings.Contains(got[0].Text, "could not be initialized") {
		t.Fatalf("got %+v, want one card for the lead", got)
	}
	if !strings.HasPrefix(got[0].Text, "The configured lead model") {
		t.Errorf("the card should open with a capital: %s", got[0].Text)
	}

	SetLeadInitError("", "", nil)
	if got = a.llmNotices(); len(got) != 0 {
		t.Errorf("nothing wrong should be no card: %+v", got)
	}
}
