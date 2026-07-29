package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A finding is usually a note about an EXTERNAL system — an API's request
// shape, an endpoint path, a library quirk — and those change without telling
// us. The hint used to be a bare date, which left the model to do the
// arithmetic and draw the conclusion; acting on a stale finding costs a wrong
// tool and a debugging detour that never suspects the memory.
func TestRecallAgeNoteGradesByAge(t *testing.T) {
	stamp := func(daysAgo int) string {
		return recallAgeNote(time.Now().AddDate(0, 0, -daysAgo).Format(time.RFC3339))
	}

	// Recent findings carry the date and nothing else — most are fine, and a
	// warning on everything is a warning on nothing.
	for _, d := range []int{0, 10, 89} {
		got := stamp(d)
		if !strings.HasPrefix(got, "(saved ") {
			t.Errorf("%dd: missing the saved date: %q", d, got)
		}
		if strings.Contains(got, "re-verify") || strings.Contains(got, "may have changed") {
			t.Errorf("%dd old should not be cautioned: %q", d, got)
		}
	}

	// A season on: flag that it may have moved, without alarm.
	mid := stamp(120)
	if !strings.Contains(mid, "may have changed since") {
		t.Errorf("120d should note possible change: %q", mid)
	}
	if strings.Contains(mid, "re-verify") {
		t.Errorf("120d should not yet demand re-verification: %q", mid)
	}

	// A year on: say what to do about it.
	old := stamp(400)
	if !strings.Contains(old, "re-verify before relying") {
		t.Errorf("400d should say what the age MEANS: %q", old)
	}
	if !strings.Contains(old, "external system") {
		t.Errorf("400d should name the class of finding at risk: %q", old)
	}
}

func TestRecallAgeNoteHandlesBadInput(t *testing.T) {
	for _, in := range []string{"", "not-a-date", "2026/07/28"} {
		if got := recallAgeNote(in); got != "" {
			t.Errorf("unparseable date %q should yield no hint, got %q", in, got)
		}
	}
	// A well-formed date always yields at least the stamp.
	if got := recallAgeNote(time.Now().Format(time.RFC3339)); got == "" {
		t.Error("a valid date should always produce the saved stamp")
	}
}

// Only findings get the hint — curated knowledge cites via its own locator,
// and dating a reference document says nothing about whether it is still true.
func TestOnlyFindingsCarryTheAgeHint(t *testing.T) {
	old := time.Now().AddDate(-2, 0, 0).Format(time.RFC3339)
	hits := []SearchHit{{ReportID: "r1", Title: "Endpoint shape", Text: "POST /v1/things", Date: old}}

	finding := renderRecallChunks("finding", "mem", hits)
	if !strings.Contains(finding, "re-verify") {
		t.Errorf("a two-year-old finding must carry the caution: %q", finding)
	}
	knowledge := renderRecallChunks("knowledge", "kb", hits)
	if strings.Contains(knowledge, "saved ") {
		t.Errorf("curated knowledge should not be age-stamped: %q", knowledge)
	}
}
