package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	. "github.com/cmcoffee/gohort/core"
)

// An agent-judged monitor's recorded checks read as its card does: the
// checker's answer, after the outcome's first ": ", said True, False or
// Issue by the rule the check fires by; a failed check is an Issue saying
// why, and one still so since it fired is True.
func TestAMonitorsChecksReadAsTheCardDoes(t *testing.T) {
	m := EventMonitor{Kind: EventKindPoll, Name: "cve-watch"}
	for _, c := range []struct {
		outcome, detail, verdict, note string
	}{
		{"no match", `answer lacks "YES": NONE`, PollFalse, ""},
		{"no match", `answer lacks "YES": Maybe, the page did not load`, PollIssue, `answered neither "YES" nor "NONE": Maybe, the page did not load`},
		{"re-armed", `no longer matches "YES": NONE - quiet again`, PollFalse, "quiet again"},
		{"held", "still matches since it fired; fires again once an answer stops matching: YES - still two CVEs", PollTrue, "YES - still two CVEs"},
		{"held", "still matches since it fired", PollTrue, ""},
		{"failed", "checker timed out", PollIssue, "the check failed: checker timed out"},
	} {
		v, n := pollCheckVerdict(m, c.outcome, c.detail)
		if v != c.verdict || n != c.note {
			t.Errorf("%s %q: %q %q, want %q %q", c.outcome, c.detail, v, n, c.verdict, c.note)
		}
	}
	// Another kind of monitor keeps its outcome as recorded.
	var http EventMonitor
	if err := json.Unmarshal([]byte(`{"name":"price","owner":"u","kind":"http_poll","recent_checks":[{"at":"2026-11-05T10:00:00Z","outcome":"no match","detail":"41.20 is not above 50"}]}`), &http); err != nil {
		t.Fatal(err)
	}
	if got := schedulerViewChecks(http); len(got) != 1 || got[0].Verdict != "" || got[0].Note != "no match: 41.20 is not above 50" {
		t.Errorf("an http monitor's check: %+v", got)
	}
}

// The View is the caller's own items only, and reads what the row does not
// carry: a schedule's runs, a monitor's checks, a task's notes.
func TestTheSchedulerViewIsTheCallersOwn(t *testing.T) {
	parkedTestSetup(t)
	SaveEventMonitor(RootDB, EventMonitor{Owner: "u", Name: "watch", Kind: EventKindPoll, LastResult: "NONE"})
	RecordRun(RootDB, RunRecord{Owner: "u", Agent: "watch", Status: RunOK, Summary: "fired once"}.AboutMonitor("watch"))
	page := readFile(t, "page_chat.go")
	if strings.Contains(page, `URL: "orchestrate_task_notes"`) || strings.Contains(page, `"Recent checks"`) {
		t.Error("a row still carries Notes or Recent checks beside View")
	}
	if strings.Count(page, `URL: "orchestrate_view_task"`) < 3 {
		t.Error("View is not on the Scheduler, the fleet Scheduler and Goals")
	}
	for _, c := range []struct{ user, kind, id string }{{"v", "monitor", "watch"}, {"u", "monitor", "nope"}, {"u", "bogus", "watch"}, {"u", "monitor", ""}} {
		if _, ok := schedulerViewFor(c.user, c.kind, c.id); ok {
			t.Errorf("%s viewed %s %q", c.user, c.kind, c.id)
		}
	}
	out, ok := schedulerViewFor("u", "monitor", "watch")
	if !ok {
		t.Fatal("the owner could not view their monitor")
	}
	runs, _ := out["runs"].([]schedulerViewRun)
	if len(runs) != 1 || runs[0].Summary != "fired once" {
		t.Errorf("the monitor's runs: %+v", out["runs"])
	}
	if _, has := out["checks"]; !has {
		t.Error("a monitor's View carries no checks")
	}
}

// A run in the View reads as the whole of what it said: its full output,
// not the feed's summary line (cut at 280) and not its first 300 runes. The
// page folds a long one with "more"; a cut here left the rest unreadable
// anywhere on the page.
func TestTheSchedulerViewShowsARunsWholeSummary(t *testing.T) {
	parkedTestSetup(t)
	SaveEventMonitor(RootDB, EventMonitor{Owner: "u", Name: "watch", Kind: EventKindPoll, LastResult: "NONE"})
	report := "Posted and verified. " + strings.Repeat("The framing and the link follow. ", 30) + "END OF REPORT"
	RecordRun(RootDB, RunRecord{Owner: "u", Agent: "watch", Status: RunOK, Summary: standingSummary(report), Raw: report}.AboutMonitor("watch"))
	out, ok := schedulerViewFor("u", "monitor", "watch")
	if !ok {
		t.Fatal("the owner could not view their monitor")
	}
	runs, _ := out["runs"].([]schedulerViewRun)
	if len(runs) != 1 || runs[0].Summary != report {
		t.Fatalf("the summary came back cut (%d of %d runes)", len([]rune(firstRunSummary(runs))), len([]rune(report)))
	}
	page := readFile(t, "assets/web_assets.html")
	if !strings.Contains(page, "item.appendChild(window.uiFoldText(said, {'class': 'orch-view-said', pre: true, lines: 4}))") {
		t.Error("the View does not fold a run's summary with uiFoldText")
	}
}

func firstRunSummary(runs []schedulerViewRun) string {
	if len(runs) == 0 {
		return ""
	}
	return runs[0].Summary
}

// The feed's summary line is cut in characters, never inside one, and ends
// in three dots.
func TestAStandingSummaryIsCutOnACharacter(t *testing.T) {
	out := strings.Repeat("stranded on‚Äî ", 40)
	got := standingSummary(out)
	if !utf8.ValidString(got) {
		t.Fatalf("the cut split a character: %q", got)
	}
	if !strings.HasSuffix(got, "...") || len([]rune(got)) > 283 {
		t.Errorf("summary = %d runes, ending %q", len([]rune(got)), got[len(got)-6:])
	}
	if standingSummary("posted") != "posted" {
		t.Error("a short reply was changed")
	}
}
