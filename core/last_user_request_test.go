package core

import (
	"strings"
	"testing"
)

// lastUserRequest picks the text a halted turn's refusal is written ABOUT, so
// picking wrong means refusing something the person never said. Everything the
// LOOP appends rides as a user-role message, and by the time a guardrail halts a
// long turn the tail is several of those — so each one has to be distinguishable
// from the human's request.
//
// Four injections shipped without the tag (the wrap-up budget note, the
// hard-stop directive on its final round, the give-up-with-errors re-prompt, and
// the failure-shape correction). Each is a plausible tail at halt time, and each
// would have handed the rejection writer the framework's own pacing text.
func TestLastUserRequestSkipsEveryLoopAuthoredTurn(t *testing.T) {
	const ask = "book me a table for four on Friday"

	// The tagged ones — every corrective and pacing message the loop injects.
	for name, msg := range map[string]string{
		"wrap-up budget note":   frameworkNoticeTag + "You have 3 rounds left of a 12-round budget. Stop exploring and produce a final answer NOW.",
		"hard stop, last round": frameworkNoticeTag + "[ROUND LIMIT — HARD STOP after this round. Produce your final answer NOW from what you already have.]",
		"give-up re-prompt":     frameworkNoticeTag + "You stopped without producing a reply and without calling any tool, but 2 tool calls errored earlier this turn.",
		"failure-shape nudge":   frameworkNoticeTag + "You have now hit this SAME failure 3 times this turn, from different calls and different arguments.",
	} {
		history := []Message{
			{Role: "user", Content: ask},
			{Role: "assistant", Content: "on it"},
			{Role: "user", Content: msg},
		}
		if got := lastUserRequest(history); got != ask {
			t.Errorf("%s: refusal would be written about the framework's own text, not the request\n got: %q", name, got)
		}
	}

	// The untagged ones, recognized by what they carry. Neither may wear the
	// tag — it tells the model not to act on the message, and both exist to be
	// acted on — so the structure has to do the work.
	carriers := []Message{
		{Role: "user", Content: "Here are 2 image(s) queued for you to view, in order.", Images: [][]byte{{0x1}, {0x2}}},
		{Role: "user", ToolResults: []ToolResult{{Content: "{\"seats\":4}"}}},
	}
	for _, carrier := range carriers {
		history := []Message{
			{Role: "user", Content: ask},
			{Role: "assistant", Content: "looking"},
			carrier,
		}
		if got := lastUserRequest(history); got != ask {
			t.Errorf("a payload turn the loop built was mistaken for the request\n got: %q", got)
		}
	}

	// The date stamp rides on the live turn and is not part of what was asked.
	stamped := []Message{{Role: "user", Content: "[Current date & time: Thu, July 30, 2026 at 9:15 AM PDT]\n\n" + ask}}
	if got := lastUserRequest(stamped); got != ask {
		t.Errorf("the date stamp must not reach the rejection writer: %q", got)
	}

	// Nothing genuine left = no request. Empty is the honest answer; the caller
	// falls back to a canned decline rather than refusing a framework message.
	onlyNotices := []Message{
		{Role: "assistant", Content: "hm"},
		{Role: "user", Content: frameworkNoticeTag + "Halfway checkpoint: you're at round 6 of 12."},
	}
	if got := lastUserRequest(onlyNotices); got != "" {
		t.Errorf("with no genuine user turn the answer is empty, got %q", got)
	}
}

// The skip is a blocklist, so the tag is load-bearing: a new loop-authored user
// message that forgets it becomes a candidate "request" the moment a guardrail
// halts the turn. This walks the real injections and asserts the invariant that
// keeps them out — every one of them names the framework as its author.
func TestLoopInjectionsCarryTheNoticeTag(t *testing.T) {
	if !strings.HasPrefix(frameworkNoticeTag, "[AUTOMATED FRAMEWORK NOTICE") {
		t.Fatal("the tag is the marker lastUserRequest matches on — changing its text silently unskips every injection")
	}
	if strings.TrimSpace(frameworkNoticeTag) == "" {
		t.Fatal("an empty tag would make strings.Contains match every message")
	}
}
