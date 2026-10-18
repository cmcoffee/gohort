package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A schedule's fire that a guardrail halted reports what happened, to the
// owner who reads it afterwards. Observed: a Moltbook fire that replied,
// deleted and upvoted had its report held back by a rule about posting links,
// and the decline writer, handed only the mission, wrote "Not running that
// engagement cycle."
func TestAHaltedRunNobodyAskedForReportsWhatItDid(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{ID: "agent-1", Name: "Poster"}, user: "owner"}
	turn.noteGuardrailRule("When posting, do not link the thread you are posting to.")

	if turn.nobodyAsked(context.Background()) {
		t.Fatal("an unmarked run is somebody's: it must decline the ordinary way")
	}
	if !turn.nobodyAsked(withNobodyAsked(context.Background(), "agent-1")) {
		t.Fatal("a fire marked for this agent was asked for by nobody")
	}
	// A sub-agent the fire dispatched is not the run that was marked: its
	// rules may be someone else's.
	if turn.nobodyAsked(withNobodyAsked(context.Background(), "agent-2")) {
		t.Error("a mark for another agent must not reach this one")
	}
	// Nor a run for somebody who is not the owner.
	other := &chatTurn{agent: AgentRecord{ID: "agent-1"}, user: "phantom:chat", ownerUser: "owner"}
	if other.nobodyAsked(withNobodyAsked(context.Background(), "agent-1")) {
		t.Error("a requester who is not the owner must get the decline")
	}

	got := turn.guardrailWithheldReport(GuardrailHalt{
		Reason:  GuardHookPreOutput,
		Request: "follow-up engagement",
		Did: []string{"moltbook/list_notifications", "moltbook/reply_to_comment", "moltbook/reply_to_comment",
			"moltbook/delete_comment", "update_notes"},
	})
	for _, want := range []string{
		"report was held back",
		`"When posting, do not link the thread you are posting to."`,
		"5 tool call(s)",
		"moltbook/reply_to_comment ×2",
		"update_notes",
		"tie it to that tool",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "follow-up engagement") {
		t.Errorf("the mission is not what the report is about:\n%s", got)
	}

	// A refused call stops the run; the binding hint is for rules judged on a
	// reply, so it is not offered there.
	got = turn.guardrailWithheldReport(GuardrailHalt{Reason: GuardHookPreAction})
	if !strings.Contains(got, "was stopped") || !strings.Contains(got, "no tool calls") || strings.Contains(got, "tie it") {
		t.Errorf("pre_action report:\n%s", got)
	}
}

// The input check of a marked fire reports instead of declining its mission,
// and never asks the decline writer (no model is wired here to ask).
func TestABlockedMissionNobodyAskedForSaysTheRunDidNotStart(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{ID: "agent-1"}, user: "owner", ctx: withNobodyAsked(context.Background(), "agent-1")}
	turn.noteGuardrailRule("Never post on weekends.")
	got := turn.guardrailInputDecline("post the weekly roundup")
	if !strings.Contains(got, "did not start") || !strings.Contains(got, "Never post on weekends.") {
		t.Errorf("got %q", got)
	}
}

func TestTallyCallsCapsALongRun(t *testing.T) {
	var calls []string
	for _, c := range "abcdefghijklmnop" {
		calls = append(calls, string(c))
	}
	got := tallyCalls(calls)
	if !strings.HasSuffix(got, "l, and 4 more") {
		t.Errorf("got %q", got)
	}
}

// The enforcement set the loop is handed picks the report for a marked fire
// and the decline writer for everything else.
func TestTheEnforcerRejectReportsOnlyForAMarkedFire(t *testing.T) {
	stub := &wardenStubLLM{reply: "Not that one."}
	turn := guardTurn(t, stub, AgentRecord{ID: "agent-1", Name: "X", Guardrails: "r", GuardrailHooks: []string{"pre_output"}})
	halt := GuardrailHalt{Reason: GuardHookPreOutput, Request: "the mission", Did: []string{"probe"}}

	if got := turn.guardrailEnforcerCtx(withNobodyAsked(context.Background(), "agent-1")).Reject(halt); !strings.Contains(got, "held back") {
		t.Errorf("a marked fire should report, got %q", got)
	}
	if got := turn.guardrailEnforcerCtx(context.Background()).Reject(halt); strings.Contains(got, "held back") {
		t.Errorf("a run somebody asked for should get the decline, got %q", got)
	}
}
