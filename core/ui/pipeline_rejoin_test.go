package ui

// A running pipeline shows where it is, every time it is looked at. Three
// ways a page came back to a run and showed it standing still or wrong: the
// status was dropped whenever a block landed (so a rejoin whose last replayed
// event was a block had no spinner at all); a run still going was opened as a
// saved record it does not have yet; and a stream kept writing into whatever
// had been opened since.

import (
	"strings"
	"testing"
)

func TestARunningPipelineKeepsShowingWhereItIs(t *testing.T) {
	b, err := PipelinePanel{SessionRunningField: "Live"}.MarshalJSON()
	if err != nil || !strings.Contains(string(b), `"session_running_field":"Live"`) {
		t.Fatalf("SessionRunningField does not reach the runtime: %s %v", b, err)
	}
	js := readRuntimeFile(t, "40_pipeline_panel.js")
	ensure := js[strings.Index(js, "function ensureBlock("):]
	ensure = ensure[:strings.Index(ensure, "\n    }\n")]
	for _, want := range []string{
		"if (prior && !runningState) prior.remove();",               // dropped only when nothing runs
		"if (prior && runningState) transcript.appendChild(prior);", // otherwise kept last
	} {
		if !strings.Contains(ensure, want) {
			t.Errorf("ensureBlock missing %q", want)
		}
	}
	for _, want := range []string{
		"var doneStatus = transcript.querySelector('.ui-pl-status');",             // done clears it
		"lastStatus.classList.replace('ui-pl-status-live', 'ui-pl-status-done');", // a stream ending early stops it where it was
		"if (s[runningF] && cfg.reconnect_url) tryReconnect(s[idF]);",             // a running row rejoins
		"function stopFollowing()",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("runtime missing %q", want)
		}
	}
	// Every way the panel starts showing something stops following the
	// stream before it: a submit, a rejoin, an open.
	for _, fn := range []string{"function streamFrom(", "function tryReconnect(", "function openSession("} {
		body := js[strings.Index(js, fn):]
		body = body[:strings.Index(body, "\n    }\n")]
		if !strings.Contains(body, "stopFollowing();") {
			t.Errorf("%s does not stop following the stream it replaces", fn)
		}
		if fn != "function openSession(" && strings.Count(body, "if (gen !== streamGen) return") < 2 {
			t.Errorf("%s does not drop a stale stream's events and ending", fn)
		}
	}
}
