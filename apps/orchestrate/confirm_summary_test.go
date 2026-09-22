package orchestrate

// The card asks somebody to judge ONE call. It used to show the tool's name,
// the raw argument blob and "this tool is set to ask before every call", none
// of which answers whether the call would be a problem.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Where it goes and what it carries decide the answer, and they used to be
// buried among the arguments that do not.
func TestTheDecisiveArgumentComesFirst(t *testing.T) {
	got := confirmCallSummary(nil, "some_tool",
		`{"retries":3,"verbose":true,"url":"https://payments.example.com/transfer","amount":"5000"}`)
	lines := strings.Split(got, "\n")
	var order []string
	for _, l := range lines {
		if k, _, ok := strings.Cut(l, ": "); ok {
			order = append(order, k)
		}
	}
	if len(order) < 4 {
		t.Fatalf("arguments were not listed: %q", got)
	}
	if order[0] != "url" {
		t.Errorf("the destination is not first, it is %q: %q", order[0], got)
	}
	// And the value is shown, not paraphrased: a summary of an argument is not
	// the argument, and somebody approving needs to see what would be sent.
	if !strings.Contains(got, "https://payments.example.com/transfer") {
		t.Error("the destination itself is missing")
	}
	if !strings.Contains(got, "5000") {
		t.Error("the amount is missing")
	}
}

// A framework tool's reach in words somebody can act on. The capability names
// are the framework's vocabulary: CapNetwork says nothing to a person deciding
// in the moment.
func TestTheCardSaysWhatTheToolCanReach(t *testing.T) {
	for _, c := range []struct {
		caps []Capability
		want string
	}{
		{[]Capability{CapNetwork}, "reaches outside this deployment"},
		{[]Capability{CapExecute}, "runs commands"},
		{[]Capability{CapWrite}, "writes files or records"},
	} {
		got := reachWords(c.caps)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("caps %v gave %v, want %q", c.caps, got, c.want)
		}
	}
	// Several read as a sentence rather than a list of jargon.
	both := joinWords(reachWords([]Capability{CapNetwork, CapWrite}))
	if !strings.Contains(both, " and ") {
		t.Errorf("multiple reaches do not read as prose: %q", both)
	}
}

// Arguments it cannot parse are shown RAW rather than dropped. A summary that
// quietly loses something is worse than the blob it replaced: the reader
// approves believing they have seen the call.
func TestAnUnparseableCallStillShowsItself(t *testing.T) {
	got := confirmCallSummary(nil, "some_tool", "not json at all")
	if !strings.Contains(got, "not json at all") {
		t.Errorf("the call vanished from its own card: %q", got)
	}
	// And an empty one does not invent content.
	if s := confirmCallSummary(nil, "some_tool", "{}"); strings.Contains(s, "{}") {
		t.Errorf("an empty argument set was rendered as a blob: %q", s)
	}
}

// A long value is bounded but never summarised, and says how much was cut so
// the reader knows there is more rather than assuming that was all of it.
func TestALongValueIsClippedAndSaysSo(t *testing.T) {
	long := strings.Repeat("A", 900)
	got := confirmCallSummary(nil, "some_tool", `{"body":"`+long+`"}`)
	if len(got) > 600 {
		t.Errorf("the card is unbounded at %d chars", len(got))
	}
	if !strings.Contains(got, "900 chars") {
		t.Errorf("the clip does not say how much is missing: %q", got)
	}
}
