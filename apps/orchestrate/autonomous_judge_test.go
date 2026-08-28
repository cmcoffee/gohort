package orchestrate

import (
	"os"
	"strings"
	"testing"
)

// A turn nobody is reading needs the claim judge MORE than one somebody is.
//
// The interactive paths had it from the start, and they are the turns where a
// person would have noticed anyway. The autonomous ones — a recurring fire, a
// standing agent's dispatch — produce a transcript nothing disputes and a run
// ledger that records a success, so a fire that writes out three finished posts,
// marks them done and calls no posting tool looks exactly like one that worked.
// That is the shape this was reported as.
//
// Guarded by reading the source because the failure is a DELETION: the hook is
// one line in a config literal, and seventeen other loop configs in this tree
// demonstrate how easily it is simply never added.
func TestAutonomousLoopsWireTheClaimJudge(t *testing.T) {
	for _, c := range []struct {
		file string
		what string
	}{
		{"scheduled_updates.go", "a recurring scheduled fire"},
		{"agent_dispatch.go", "a dispatched / standing agent run"},
	} {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("reading %s: %v", c.file, err)
		}
		if !strings.Contains(string(src), "TurnClaimJudge:") {
			t.Errorf("%s no longer wires TurnClaimJudge — %s can now claim work it did not do, "+
				"and there is nobody on that path to notice", c.file, c.what)
		}
	}
}
