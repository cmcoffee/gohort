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
	"strings"
	"testing"
)

// An empty store must read as "the evidence is not loaded", never as "your
// search found nothing". The second is a negative an LLM will report as fact:
// it answers "was there an OOM?" with "no" on a bundle nobody uploaded.
func TestBundleToolsReportNotIngestedRatherThanEmpty(t *testing.T) {
	for _, td := range BundleTools("u1", "empty") {
		args := map[string]any{}
		switch td.Tool.Name {
		case "search_bundle":
			args["pattern"] = "anything"
		case "read_bundle_file":
			args["path"] = "var/log/messages"
		}
		out, err := td.Handler(args)
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
	for _, td := range BundleTools("u1", "b1") {
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
