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
		"if (recordLocked && !answerPass) return;", // nothing is sent into a record but an answer
		"sendBtn.disabled = recordLocked;",         // a finished run does not unlock it
		"applyRecordLock(sid);",                    // every open decides
	} {
		if !strings.Contains(js, want) {
			t.Errorf("runtime missing %q", want)
		}
	}
}

// A pinned home thread can be read-only too (AltLocked), and a question card
// still answers into it: the card dispatches ui-ask-answer, which the panel
// sends through the lock, so a run waiting in that thread is never stuck.
func TestALockedHomeThreadStillTakesAnswers(t *testing.T) {
	raw, _ := json.Marshal(AgentLoopPanel{AltLocked: true, AltLockedText: "read here"})
	for _, want := range []string{`"alt_locked":true`, `"alt_locked_text":"read here"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s: %s", want, raw)
		}
	}
	js := string(runtimeJS)
	for _, want := range []string{
		"cfg.alt_locked && sid && sid === altPinnedSession(agentId)",
		"inputArea.addEventListener('ui-ask-answer'",
		"answerPass = true;",
		"new CustomEvent('ui-ask-answer'",
		"inputRow.style.display = recordLocked ? 'none' : '';", // no composer on a locked thread
		"class: 'ui-agent-locked-note'",                        // a line in its place
		"if (!inputArea.dispatchEvent(ev)) return true;",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("runtime missing %q", want)
		}
	}
}
