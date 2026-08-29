package admin

// Editing a per-call cost has to move the chart without a page reload.
//
// The plumbing was already there and unused: a chart refetches when something
// invalidates its exact source string, and none of the four forms that can
// change a cost said anything. So the number changed, the graph did not, and
// the only way to see the truth was a full refresh.

import (
	"os"
	"strings"
	"testing"
)

// Matching is on the EXACT source string, which is why both the chart and the
// forms read one constant. "days=30" in the chart and "days=60" in a form's
// Invalidate would fail silently and leave a stale graph with nothing to notice
// it by, so the coupling is a compile error rather than a bug report.
func TestCostSurfacesAreNamedOnce(t *testing.T) {
	b, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, lit := range []string{`"api/cost-history?days=`, `"api/cost-by-source?days=`} {
		if n := strings.Count(src, lit); n > 1 {
			t.Errorf("%s appears as a literal %d times; use the shared constant or the two will drift", lit, n)
		}
	}
}

// Every form that can set a per-call cost must refresh the surfaces that count
// it. Both the create and the edit form, for hooks and for credentials: a cost
// typed while creating a source hook is as invisible as one typed while editing
// it.
func TestEveryCostEditingFormInvalidates(t *testing.T) {
	b, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	// The four forms are the ones whose fields carry cost_per_call.
	forms := strings.Count(src, "credentialFormFields()") + strings.Count(src, "sourceHookFormFields()")
	forms -= 2 // the two func declarations themselves
	if forms != 4 {
		t.Fatalf("expected 4 cost-editing forms, found %d; the invalidation count below is calibrated to that", forms)
	}
	if got := strings.Count(src, "costSources"); got < forms {
		t.Errorf("costSources named %d time(s) for %d forms that can change a cost: one of them updates the number and not the chart", got, forms)
	}
}
