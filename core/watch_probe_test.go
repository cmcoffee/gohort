package core

import (
	"context"
	"strings"
	"testing"
)

// The watcher's own half of the probe. The sandbox half — that a script's stdin
// carries {prior,current} and its stdout comes back — moved out with the
// mechanics (core/sandbox); what belongs here is that the WATCHER, calling the
// same path, does not suppress a script that printed something.
func TestFormatWatchAlertKeepsWhatAScriptPrinted(t *testing.T) {
	script := `import sys, json
d = json.load(sys.stdin)
print("prior_len=%d current_len=%d" % (len(d["prior"]), len(d["current"])))`

	summary, suppress := formatWatchAlert(context.Background(), "tester", "probe", script,
		"No clients connected.", `[{"clid":"170"}]`, false)
	t.Logf("formatWatchAlert summary=%q suppress=%v", summary, suppress)
	if suppress || strings.TrimSpace(summary) == "" {
		t.Fatalf("formatWatchAlert suppressed a script that prints output: suppress=%v summary=%q", suppress, summary)
	}
}
