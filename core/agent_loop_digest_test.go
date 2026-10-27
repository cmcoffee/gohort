package core

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The section ranking names a loop clause as its own section, instead of
// counting it under the last heading before it: the clauses come after every
// heading, so 8k of standing rules read as "Tools available" or "Other phases
// in this workflow".
func TestTheSectionRankingNamesTheLoopClauses(t *testing.T) {
	prompt := "Persona.\n\n## Tools available\nweb_search, fetch_url\n\n[Grounding: state a precise specific only when " + strings.Repeat("x", 400) + "]\n\n[Secrets: never ask for a key]"
	got := systemSectionRanking(prompt)
	for _, want := range []string{`"[Grounding]"=`, `"[Secrets]"=`, `"Tools available"=`} {
		if !strings.Contains(got, want) {
			t.Errorf("ranking lacks %s: %s", want, got)
		}
	}
	if strings.Index(got, `"[Grounding]"`) > strings.Index(got, `"Tools available"`) {
		t.Errorf("the long clause should outrank the short section: %s", got)
	}
}

// The zero-tool-turn diagnostic.
//
// A turn that called nothing leaves no trail but its own words. Observed:
// "Wiwee, try again" answered in 66 characters with zero tool calls — and the
// framework recorded the length and nothing else, so whether that reply was an
// honest refusal or a fresh empty promise could not be established afterwards
// at all. Every other shape of turn is reconstructable from its tool calls.

func TestTheZeroToolDiagnosticCarriesBothSides(t *testing.T) {
	line := noToolDiagLine(1, "Wiwee, try again", "Yeah, that one's still not working out.", false)

	// Both sides, because either alone is unreadable: the reply without the
	// request is a sentence with no question attached.
	if !strings.Contains(line, "try again") {
		t.Errorf("the request must be logged:\n%s", line)
	}
	if !strings.Contains(line, "still not working out") {
		t.Errorf("the reply must be logged — it is the whole point:\n%s", line)
	}
	// Greppable alongside COLLAPSE-DIAG, which is the neighbouring diagnostic.
	if !strings.Contains(line, "NOTOOL-DIAG round 1") {
		t.Errorf("the line must be greppable and name its round:\n%s", line)
	}
}

func TestAMaskedSessionLeaksNothing(t *testing.T) {
	// MaskDebugOutput exists because some sessions carry SSH credentials,
	// system facts and private files. A diagnostic is not worth leaking them,
	// and "we only log the reply, not the tool results" is not an argument —
	// the reply is where a model repeats what it just read.
	line := noToolDiagLine(2, "the root password is hunter2", "I've stored hunter2 for you.", true)

	for _, secret := range []string{"hunter2", "root password", "stored"} {
		if strings.Contains(line, secret) {
			t.Errorf("masked output leaked %q:\n%s", secret, line)
		}
	}
	// Still useful: lengths and the round survive, so the turn is still
	// visible as having happened.
	if !strings.Contains(line, "NOTOOL-DIAG round 2") || !strings.Contains(line, "masked") {
		t.Errorf("a masked line must still record the turn:\n%s", line)
	}
}

func TestTruncationNeverSplitsACharacter(t *testing.T) {
	// The replies that prompted all of this end in "🏚️👔". Slicing bytes at a
	// fixed offset writes invalid UTF-8 into the log, which was theoretical
	// while this only truncated tool names and stopped being the moment it
	// started carrying reply text.
	emoji := strings.Repeat("🏚️", 200) + "tail"
	for _, n := range []int{1, 2, 3, 7, 64, 199, 1000} {
		got := truncForLog(emoji, n)
		if !utf8.ValidString(got) {
			t.Errorf("truncForLog(…, %d) produced invalid UTF-8: %q", n, got)
		}
	}
	// Short input is returned untouched, ellipsis and all.
	if got := truncForLog("short", 1000); got != "short" {
		t.Errorf("untruncated input must pass through, got %q", got)
	}
	// Newlines are flattened so one turn is one log line.
	if got := truncForLog("a\nb", 10); got != "a b" {
		t.Errorf("newlines must flatten to keep the line greppable, got %q", got)
	}
}

// The ranking must order by serialized size, largest first, and its total must
// be the sum of what it lists — otherwise it is a third tools figure that
// agrees with neither the floor line nor the prompt~ line.
func TestToolSchemaRankingOrdersLargestFirst(t *testing.T) {
	small := AgentToolDef{Tool: Tool{Name: "small", Description: "x"}}
	big := AgentToolDef{Tool: Tool{Name: "big", Description: strings.Repeat("y", 500)}}
	got := toolSchemaRanking([]AgentToolDef{small, big})
	if !strings.Contains(got, "2 tools,") {
		t.Fatalf("count missing: %s", got)
	}
	bi, si := strings.Index(got, " big="), strings.Index(got, " small=")
	if bi < 0 || si < 0 || bi > si {
		t.Fatalf("want big before small: %s", got)
	}
	if toolSchemaRanking(nil) != "" {
		t.Fatalf("empty catalog should report nothing")
	}
}

// Every byte of the system prompt lands in exactly one section, so the rows
// sum to the prompt's size; text before the first heading is the preamble; and
// the largest section is listed first.
func TestSystemSectionRankingCoversEveryByte(t *testing.T) {
	sys := "You are helpful.\n\n## Small\nx\n## Big\n" + strings.Repeat("y", 200) + "\n#!/bin/sh\n#include <x>\n"
	got := systemSectionRanking(sys)
	if !strings.Contains(got, "3 sections,") || !strings.Contains(got, fmt.Sprintf("%d bytes", len(sys))) {
		t.Fatalf("count or total wrong: %s", got)
	}
	bi, si, pi := strings.Index(got, `"Big"=`), strings.Index(got, `"Small"=`), strings.Index(got, `"(preamble)"=`)
	if bi < 0 || si < 0 || pi < 0 || bi > pi || bi > si {
		t.Fatalf("want Big first and all three present: %s", got)
	}
	sum := 0
	for _, f := range strings.Fields(got[strings.Index(got, "first: ")+7:]) {
		var n int
		fmt.Sscanf(f[strings.LastIndex(f, "=")+1:], "%d", &n)
		sum += n
	}
	if sum != len(sys) {
		t.Fatalf("sections sum to %d, prompt is %d", sum, len(sys))
	}
	if systemSectionRanking("  ") != "" {
		t.Fatal("an empty prompt should report nothing")
	}
}

// A backend that reports nothing must add nothing to the breadcrumb — the
// whole local fleet is llama.cpp today, but the hosted providers are not, and
// a "0/0 prompt tokens (0% cached)" on every Anthropic round would read as a
// cache failure that never happened.
func TestPromptReuseNoteSilentWithoutServerTimings(t *testing.T) {
	for _, resp := range []*Response{
		nil,
		{InputTokens: 16000},
		{PromptTokensPrefilled: 40},
	} {
		if got := promptReuseNote(resp); got != "" {
			t.Fatalf("expected no note without timings, got %q", got)
		}
	}
}

// The reused case is the one worth reading: a stable prefix means the server
// prefills only the new tokens, and the note has to make that obvious at a
// glance rather than requiring the reader to do the subtraction.
func TestPromptReuseNoteReportsCachedShare(t *testing.T) {
	got := promptReuseNote(&Response{InputTokens: 16000, PromptTokensPrefilled: 160, PrefillMS: 210})
	if !strings.Contains(got, "160/16000") {
		t.Fatalf("missing prefilled/total: %q", got)
	}
	if !strings.Contains(got, "99% cached") {
		t.Fatalf("expected 99%% cached, got %q", got)
	}
	if !strings.Contains(got, "210ms") {
		t.Fatalf("missing prefill wall-time: %q", got)
	}
}

// A cold turn re-prefills everything. It must report 0% rather than dividing
// by a zero or going negative, because this is exactly the reading someone
// takes after a prefix-stability regression.
func TestPromptReuseNoteColdPrefillReportsZeroCached(t *testing.T) {
	got := promptReuseNote(&Response{InputTokens: 16000, PromptTokensPrefilled: 16000, PrefillMS: 2839})
	if !strings.Contains(got, "0% cached") {
		t.Fatalf("expected 0%% cached on a full prefill, got %q", got)
	}
}

// prompt_n above prompt_tokens is not physical, but a backend reporting the
// two from different counters could still emit it; the note must not print a
// negative cache share.
func TestPromptReuseNoteClampsOverReportedPrefill(t *testing.T) {
	got := promptReuseNote(&Response{InputTokens: 100, PromptTokensPrefilled: 140})
	if strings.Contains(got, "-") {
		t.Fatalf("negative cache share leaked: %q", got)
	}
}
