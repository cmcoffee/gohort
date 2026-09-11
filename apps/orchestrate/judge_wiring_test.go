// Both judges, every dispatch site.
//
// The claim judge asks whether a reply is true about what the turn DID; the
// grounding judge asks whether it knows what it ASSERTS. They answer different
// questions and are wired at the same four places, and for a while one site had
// only the first — an inconsistency rather than a decision, and the invisible
// kind: the path still worked, it just stopped checking half of what the others
// check.
//
// A count, not a behaviour test, because the failure is an omission at a call
// site and nothing about a passing turn reveals it.
package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBothJudgesAreWiredAtEverySite(t *testing.T) {
	claim, grounding := 0, 0
	for _, name := range []string{"agent_dispatch.go", "runner.go", "agents_grouped_tool.go", "scheduled_updates.go"} {
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			// The assignment or the struct field, not the type or the builder.
			if strings.Contains(trimmed, "TurnClaimJudge:") || strings.Contains(trimmed, "TurnClaimJudge =") {
				claim++
			}
			if strings.Contains(trimmed, "TurnGroundingJudge:") || strings.Contains(trimmed, "TurnGroundingJudge =") {
				grounding++
			}
		}
	}
	if claim == 0 {
		t.Fatal("no claim-judge wiring found — the scan is looking in the wrong place")
	}
	if grounding != claim {
		t.Errorf("the two judges should be wired at the same sites: claim=%d grounding=%d", claim, grounding)
	}
}

// Scope without a judge is inert, and a judge without scope never fires. They
// have to arrive together.
func TestGroundingScopeAccompaniesTheJudge(t *testing.T) {
	judge, scope := 0, 0
	for _, name := range []string{"agent_dispatch.go", "runner.go"} {
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(trimmed, "TurnGroundingJudge:") || strings.Contains(trimmed, "TurnGroundingJudge =") {
				judge++
			}
			if strings.Contains(trimmed, "UncheckedClaims:") || strings.Contains(trimmed, "UncheckedClaims =") {
				scope++
			}
		}
	}
	if judge != scope {
		t.Errorf("every grounding judge needs its scope: judge=%d scope=%d", judge, scope)
	}
}

// What came BEFORE this turn, wherever the path has a thread to read it from.
//
// The claim judge sees ONE turn, so a reply recapping work the conversation
// already did arrives looking exactly like one inventing it — whether that work
// was an earlier turn's tool call (PriorTurnWork) or an earlier scheduled run's
// report filed into the thread (PriorReports). Three sites run the judge over a
// persisted thread — the two interactive loops and the scheduled fire — and each
// has to hand it both, or the recap is convicted on the absence. A count, for
// the same reason as above: the failure is an omission at a call site, and every
// turn still passes.
//
// agent_dispatch.go / agents_grouped_tool.go are deliberately not in the list.
// A dispatch answers on a sub-thread built for the run, so "earlier turns of
// this conversation" is a set it does not have.
func TestPriorContextIsWiredWhereverThereIsAThread(t *testing.T) {
	for _, name := range []string{"runner.go", "runner_worker.go", "scheduled_updates.go"} {
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		found := map[string]bool{}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, field := range []string{"PriorTurnWork", "PriorReports"} {
				if strings.Contains(trimmed, field+":") || strings.Contains(trimmed, field+" =") {
					found[field] = true
				}
			}
		}
		for _, field := range []string{"PriorTurnWork", "PriorReports"} {
			if !found[field] {
				t.Errorf("%s runs the claim judge over a persisted thread and must supply %s", name, field)
			}
		}
	}
}
