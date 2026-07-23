package core

import (
	"strings"
	"testing"
)

// TestNormalizeFailureShapeMatchesAcrossArgs is the core of the failure-shape
// guard: the CalDAV turn hit one wall from four different call signatures
// (two tools × two date ranges) and the signature-keyed guards never tripped.
// The failure TEXT was identical every time, so the shapes must match.
func TestNormalizeFailureShapeMatchesAcrossArgs(t *testing.T) {
	a := normalizeFailureShape("Error: Failed to create calendar: [exit: exit status 1]")
	b := normalizeFailureShape("Error: Failed to create calendar: [exit: exit status 1]")
	if a == "" || a != b {
		t.Fatalf("identical failures must share a shape; got %q vs %q", a, b)
	}
	// Same wall, different surrounding whitespace/case — still one shape.
	c := normalizeFailureShape("  ERROR: Failed to create calendar:  [exit: exit status 1]\n")
	if c != a {
		t.Fatalf("case/whitespace must not split the shape; got %q vs %q", c, a)
	}
}

// TestNormalizeFailureShapeCollapsesVolatileIDs confirms one failure carrying a
// rotating id is still counted as one wall, not as N distinct failures.
func TestNormalizeFailureShapeCollapsesVolatileIDs(t *testing.T) {
	a := normalizeFailureShape("run 88421903 not found for approval 55120")
	b := normalizeFailureShape("run 99333817 not found for approval 77410")
	if a != b {
		t.Fatalf("long digit runs must collapse to one shape; got %q vs %q", a, b)
	}
}

// TestNormalizeFailureShapeKeepsDistinctFailuresApart guards the other
// direction: over-normalizing would merge unrelated walls and trip the guard
// on a turn that is actually making progress.
func TestNormalizeFailureShapeKeepsDistinctFailuresApart(t *testing.T) {
	a := normalizeFailureShape("Error: Failed to create calendar: [exit: exit status 1]")
	b := normalizeFailureShape(`ERROR: agent "Gohort" has no attached tools to run`)
	if a == b {
		t.Fatalf("distinct failures must not share a shape: %q", a)
	}
}

// TestNormalizeFailureShapeSkipsTooShort confirms a result too small to
// fingerprint is ignored rather than counted as a shape everything matches.
func TestNormalizeFailureShapeSkipsTooShort(t *testing.T) {
	for _, s := range []string{"", "   ", "nope", "error: 42"} {
		if got := normalizeFailureShape(s); got != "" {
			t.Errorf("%q should not fingerprint; got %q", s, got)
		}
	}
}

// TestNormalizeFailureShapeTruncates keeps the fingerprint bounded so a huge
// error body doesn't sit in the map (and so a varying tail can't split a wall).
func TestNormalizeFailureShapeTruncates(t *testing.T) {
	long := "upstream rejected the request: " + strings.Repeat("detail ", 200)
	got := normalizeFailureShape(long)
	if len(got) > 160 {
		t.Fatalf("shape must be capped at 160 chars, got %d", len(got))
	}
	// The varying tail beyond the cap must not split one wall into many.
	other := normalizeFailureShape(long + " trailing difference 12")
	if got != other {
		t.Fatalf("tail beyond the cap must not change the shape")
	}
}

// TestOneLineShape covers the log/directive rendering.
func TestOneLineShape(t *testing.T) {
	if got := oneLineShape("short"); got != "short" {
		t.Errorf("short shapes pass through, got %q", got)
	}
	long := strings.Repeat("x", 200)
	got := oneLineShape(long)
	if len([]rune(got)) != 121 || !strings.HasSuffix(got, "…") {
		t.Errorf("long shapes truncate with an ellipsis, got %d runes", len([]rune(got)))
	}
}

// TestLeadTurnTokenBudgetDefault pins the default cap as enabled — a zero
// value silently disables the guard, which is a mistake worth catching here.
func TestLeadTurnTokenBudgetDefault(t *testing.T) {
	if LeadTurnTokenBudget <= 0 {
		t.Fatal("lead turn budget must default to a positive cap")
	}
}

// TestNormalizeFailureShapeKeepsShortNumbers guards the digit handling: long
// runs collapse (rotating ids) but short ones are preserved verbatim — "exit
// status 1" and "exit status 2" are different failures, and the shape is
// quoted back to the model, so it must not misreport what it saw.
func TestNormalizeFailureShapeKeepsShortNumbers(t *testing.T) {
	got := normalizeFailureShape("Failed to create calendar: [exit: exit status 1]")
	if !strings.Contains(got, "exit status 1") {
		t.Fatalf("short numbers must survive verbatim; got %q", got)
	}
	other := normalizeFailureShape("Failed to create calendar: [exit: exit status 2]")
	if got == other {
		t.Fatalf("different exit codes are different failures; both normalized to %q", got)
	}
}
