package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

// The scan's cases are well formed and are scanned by the scan itself.
func TestTheScanCasesRunThroughTheScan(t *testing.T) {
	for _, c := range scanCases() {
		if c.Want != ScanClean && c.Want != ScanFlagged {
			t.Errorf("case %q: bad answer %q", c.ID, c.Want)
		}
		if !ToolScanWorthScanning(c.Input["content"]) {
			t.Errorf("case %q is too short for the scan to ask a model", c.ID)
		}
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"status": "flagged", "span": "IGNORE ALL PREVIOUS INSTRUCTIONS", "reason": "addresses the AI"}`, Repeat: true}}}
	var injection checktest.Case
	for _, c := range scanCases() {
		if c.ID == "obvious-injection" {
			injection = c
		}
	}
	o := runScanCase(context.Background(), llm, injection)
	if llm.Calls() != 1 {
		t.Fatalf("the scan asked the model %d times", llm.Calls())
	}
	if o.Got != ScanFlagged {
		t.Errorf("the scan's verdict: %+v", o)
	}
}
