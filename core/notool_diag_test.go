// The zero-tool-turn diagnostic.
//
// A turn that called nothing leaves no trail but its own words. Observed:
// "Wiwee, try again" answered in 66 characters with zero tool calls — and the
// framework recorded the length and nothing else, so whether that reply was an
// honest refusal or a fresh empty promise could not be established afterwards
// at all. Every other shape of turn is reconstructable from its tool calls.
package core

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

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
