package core

// The bundle tools' own promises, as opposed to the store's (core/bundle) or
// servitor's use of them (apps/servitor).
//
// Note what deliberately did NOT move here: the check that every tool name is
// on servitor's worker allow-list. That list is a hardcoded map of strings in
// apps/servitor/tool_guard.go, so the assertion is about whether the two agree
// — and it has to run where the list is. Renaming a tool here without renaming
// it there is exactly the drift it exists to catch.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/bundle"
)

// An empty store must read as "the evidence is not loaded", never as "your
// search found nothing". The second is a negative an LLM will report as fact:
// it answers "was there an OOM?" with "no" on a bundle nobody uploaded.
func TestBundleToolsReportNotIngestedRatherThanEmpty(t *testing.T) {
	for _, td := range BundleTools(context.Background(), "u1", "empty") {
		args := map[string]any{}
		switch td.Tool.Name {
		case "search_bundle":
			args["pattern"] = "anything"
		case "read_bundle_file":
			args["path"] = "var/log/messages"
		}
		out, err := td.Handler(context.Background(), args)
		if err != nil {
			t.Errorf("%s on an empty store errored: %v", td.Tool.Name, err)
			continue
		}
		if !strings.Contains(out, "BUNDLE NOT INGESTED") {
			t.Errorf("%s on an empty store returned %q — it must say the bundle is not ingested", td.Tool.Name, out)
		}
	}
}

// The full set is five. Asserted here as well as against servitor's allow-list
// because the two catch different things: this one notices a tool that stopped
// being built, that one notices a tool the app will refuse to run.
func TestBundleToolsAreTheFullSet(t *testing.T) {
	want := map[string]bool{
		"bundle_summary": true, "list_bundle": true, "search_bundle": true,
		"read_bundle_file": true, "bundle_timeline": true,
	}
	got := map[string]bool{}
	for _, td := range BundleTools(context.Background(), "u1", "b1") {
		got[td.Tool.Name] = true
	}
	if len(got) != len(want) {
		t.Errorf("BundleTools returned %d tools, want %d: %v", len(got), len(want), got)
	}
	for n := range want {
		if !got[n] {
			t.Errorf("tool %q is missing", n)
		}
	}
}

// An unreadable time window must fail loudly. Silently ignoring it turns
// "nothing happened in that hour" into a confident, wrong answer drawn from the
// whole bundle.
func TestParseBundleArgTimeRejectsGarbage(t *testing.T) {
	if _, err := ParseBundleArgTime("last tuesday"); err == nil {
		t.Error("an unparseable time was accepted")
	}
	if ts, err := ParseBundleArgTime(""); err != nil || !ts.IsZero() {
		t.Error("an empty time should mean no restriction, with no error")
	}
	for _, s := range []string{"2026-03-14", "2026-03-14 02:00", "2026-03-14 02:00:00", "2026-03-14T02:00:00Z"} {
		if _, err := ParseBundleArgTime(s); err != nil {
			t.Errorf("%q was rejected: %v", s, err)
		}
	}
}

// A timeline keeps its EARLIEST lines, so one that was cut stops partway
// through the story: the reply has to say so and where to pick it up, and say
// what the pattern matched but could not place.
func TestBundleTimelineReplySaysWhereItWasCut(t *testing.T) {
	at := time.Date(2026, 3, 14, 2, 14, 9, 0, time.UTC)
	res := bundle.TimelineResult{
		Entries: []bundle.TimelineEntry{{When: at.Add(-time.Second), Path: "a.log", Line: 1, Text: "x"}, {When: at, Path: "b.log", Line: 4, Text: "y"}},
		Total:   9, Undated: 2, Excluded: 3,
	}
	out := renderBundleTimeline(res, bundle.TimelineQuery{Pattern: "req=7f3a"})
	for _, want := range []string{
		"CUT: these are the earliest 2 of 9 lines. To read on, repeat with since set to 2026-03-14 02:14:09",
		"3 more line(s) matched but were left out by exclude",
		"2 line(s) matched the pattern but carry no timestamp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("reply is missing %q:\n%s", want, out)
		}
	}
	if out := renderBundleTimeline(bundle.TimelineResult{Total: 0}, bundle.TimelineQuery{}); strings.Contains(out, "CUT") {
		t.Errorf("an uncut timeline says it was cut:\n%s", out)
	}
}

// search_bundle takes over_time and bucket from its arguments, and a bad
// bucket is an error the caller can fix, not an empty reply.
func TestSearchBundleOffersOverTime(t *testing.T) {
	var search AgentToolDef
	for _, td := range BundleTools(context.Background(), "u1", "b1") {
		if td.Tool.Name == "search_bundle" {
			search = td
		}
	}
	for _, p := range []string{"over_time", "bucket"} {
		if _, ok := search.Tool.Parameters[p]; !ok {
			t.Errorf("search_bundle has no %q parameter", p)
		}
	}
	h := bundle.NewHistogram()
	h.Add(time.Date(2026, 3, 14, 2, 14, 0, 0, time.UTC))
	out := renderBundleSearch(bundle.SearchResult{Histogram: h, Scanned: 2, Excluded: 1}, bundle.Query{Pattern: "timeout", OverTime: true})
	for _, want := range []string{`"timeout": 1 matching line(s)`, "(2 files scanned.)", "left out by exclude"} {
		if !strings.Contains(out, want) {
			t.Errorf("reply is missing %q:\n%s", want, out)
		}
	}
}

// A long search_bundle reply arrives as its first page with the summary on it,
// and read_output reaches the hits past that page without searching again.
func TestSearchBundlePagesThroughReadOutput(t *testing.T) {
	var hits []bundle.Hit
	for i := 1; i <= 900; i++ {
		hits = append(hits, bundle.Hit{Path: "app.log", Line: i, Text: fmt.Sprintf("ERROR request %d failed upstream", i)})
	}
	res := bundle.SearchResult{Hits: hits, Scanned: 1}
	res.Max, res.Kept, res.Total = 900, 900, 1200
	res.Truncated = res.Cut()
	reply := SpillOutput(renderBundleSearch(res, bundle.Query{Pattern: "error"}), bundle.SearchPageChars, "read_output")
	if !strings.HasPrefix(reply, "Showing 900 of 1200 matches") {
		t.Fatalf("the first page should open with the summary:\n%.300s", reply)
	}
	m := regexp.MustCompile(`output_id="([^"]+)"`).FindStringSubmatch(reply)
	if m == nil || strings.Contains(reply, "request 800 ") {
		t.Fatalf("a long reply should arrive as its first page with a read_output trailer:\n%s", reply[len(reply)-300:])
	}
	found, err := OutputPage{ID: m[1], Grep: "request 800 ", Tool: "read_output"}.Read()
	if err != nil || !strings.Contains(found, "app.log:800:") {
		t.Errorf("grep within the kept reply should reach hit 800: %v\n%s", err, found)
	}
}
