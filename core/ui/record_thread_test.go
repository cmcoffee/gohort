package ui

// A record thread: an app-named map of agent -> thread, pinned at the top of
// the session list and opened read-only, without swapping the sidebar the way
// the alternate nav does.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecordThreadOptionsReachThePanel(t *testing.T) {
	raw, _ := json.Marshal(AgentLoopPanel{RecordNavFlag: "APP_RECORDS", RecordLabel: "Log", RecordHint: "h", RecordLockedText: "read only"})
	for _, want := range []string{`"record_nav_flag":"APP_RECORDS"`, `"record_label":"Log"`, `"record_locked_text":"read only"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s: %s", want, raw)
		}
	}
	raw, _ = json.Marshal(AgentLoopPanel{})
	if strings.Contains(string(raw), "record_") {
		t.Errorf("unused record options should be omitted: %s", raw)
	}
}

func TestTheRuntimeLocksARecordThread(t *testing.T) {
	js := string(runtimeJS)
	for _, want := range []string{
		"function recordPinnedSession(agentId)",
		"if (recordLocked) return;",        // nothing is sent into a record
		"sendBtn.disabled = recordLocked;", // a finished run does not unlock it
		"applyRecordLock(sid);",            // every open decides
	} {
		if !strings.Contains(js, want) {
			t.Errorf("runtime missing %q", want)
		}
	}
}
