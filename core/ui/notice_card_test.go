package ui

// The conversation pane shows a framework breadcrumb where it happened. The
// panel is generic about it: a level, a kind slug and a sentence, all supplied
// by whoever is driving the stream. Nothing here knows what a guard is.

import (
	"strings"
	"testing"
)

func TestNoticeFramesReachTheConversation(t *testing.T) {
	js := runtimeJS
	if !strings.Contains(js, "case 'notice':") {
		t.Fatal("the panel ignores notice frames — a block would stream past and land nowhere")
	}
	if !strings.Contains(js, "function addNotice(") || !strings.Contains(js, "function buildNotice(") {
		t.Fatal("notice rendering is missing")
	}
	// The card is built ONCE and used by both routes, so a replayed block and
	// a live one cannot drift into looking like different things.
	if !strings.Contains(js, "function replayNotices(") || !strings.Contains(js, "buildNotice({level: e.level") {
		t.Error("replay does not go through the same card builder as the live path")
	}
}

// A blocked request must not get to style the notice that says it was blocked.
func TestNoticeBodyIsNotMarkdown(t *testing.T) {
	js := runtimeJS
	i := strings.Index(js, "function buildNotice(")
	if i < 0 {
		t.Fatal("buildNotice missing")
	}
	body := js[i:]
	if j := strings.Index(body, "\n    function "); j > 0 {
		body = body[:j]
	}
	if strings.Contains(body, "uiRenderMarkdown") || strings.Contains(body, "innerHTML") {
		t.Error("the notice body renders markup; the detail quotes whatever tripped the guard")
	}
	if !strings.Contains(body, "body.textContent = ev.text") {
		t.Error("the notice body must be set as text")
	}
}

// A page that loads mid-run receives the same breadcrumb twice by two honest
// routes — the run buffer replays from sequence zero, and the trail replay
// places the same entry among the messages. Shown once.
func TestNoticesAreShownOnce(t *testing.T) {
	js := runtimeJS
	if !strings.Contains(js, "var noticeIds = {};") {
		t.Fatal("no dedup ledger for notices")
	}
	if !strings.Contains(js, "if (noticeIds[ev.id]) return;") {
		t.Error("the live path does not check the ledger")
	}
	if !strings.Contains(js, "if (e.id && noticeIds[e.id]) return false;") {
		t.Error("the replay path does not check the ledger")
	}
	// Cleared with the rest of the per-thread state, or switching threads
	// would suppress the next thread's breadcrumbs.
	if !strings.Contains(js, "msgEls = {}; activityEls = {}; blockEls = {}; noticeIds = {};") {
		t.Error("the ledger survives a thread switch")
	}
}

// Only blocking entries take a card. The trail holds the rest, which is where
// a note nobody has to act on belongs.
func TestOnlyBlockingEntriesReplayIntoTheFlow(t *testing.T) {
	if !strings.Contains(runtimeJS, "e.level !== 'blocked'") {
		t.Error("replay does not filter by level — every retry and fold would take a card")
	}
}

func TestNoticeHasItsOwnChrome(t *testing.T) {
	if !strings.Contains(runtimeCSS, ".ui-agent-notice {") {
		t.Fatal("no styling for the notice card")
	}
	// Themed, not hardcoded: the four themes carry different warning colors.
	if !strings.Contains(runtimeCSS, "var(--warning,") {
		t.Error("the notice ignores the theme's warning color")
	}
}
