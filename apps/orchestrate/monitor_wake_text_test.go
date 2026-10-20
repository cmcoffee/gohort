package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The wake a monitor writes is the wake the history recognises: they share one
// prefix, so a run of wakes folds. A wording sweep once gave the writer a comma
// and the reader a colon, and none folded after that. The older spellings stay
// recognised for wakes already stored.
func TestAMonitorWakeIsRecognisedAsOne(t *testing.T) {
	written := monitorWakeMessage(EventMonitor{}, "timer-0447pm", "Timer went off.", "")
	if !isMonitorWakeText(written) {
		t.Fatalf("the wake as written is not recognised: %q", written[:60])
	}
	for _, old := range []string{"[EVENT: monitor \"x\" fired]", "[EVENT — monitor \"x\" fired]"} {
		if !isMonitorWakeText(old) {
			t.Errorf("a stored wake is not recognised: %q", old)
		}
	}
	if isMonitorWakeText("[EVENT: bridge \"x\" fired]") || isMonitorWakeText("what fired?") {
		t.Error("something other than a monitor wake was taken for one")
	}
	var msgs []ChatMessage
	for i := 0; i < 6; i++ {
		msgs = append(msgs, ChatMessage{Role: "user", Content: written}, ChatMessage{Role: "assistant", Content: "noted"})
	}
	if got := collapseMonitorWakes(msgs, 2); len(got) >= len(msgs) {
		t.Errorf("a run of six wakes did not fold: %d messages left of %d", len(got), len(msgs))
	}
}
