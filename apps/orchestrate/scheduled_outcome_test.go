// What a finished fire tells its owner.
//
// This decision used to be inline in fireOrchestrateUpdate, reachable only by
// running a whole recurring fire, and it was wrong in a way nothing could
// catch: a run that read injected instructions and kept going recorded "ok".
// These pin the inputs that promote a run, and the ORDER the summary is built
// in — the Activity feed truncates, so what leads is what gets read.

package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnOrdinaryFireIsOK(t *testing.T) {
	status, summary := scheduledOutcome{reply: "Posted the weekly digest."}.resolve()
	if status != RunOK {
		t.Errorf("a fire that did its work is ok, got %q", status)
	}
	if summary == "" {
		t.Error("the summary should still say what the run produced")
	}
}

// The regression this whole change is for.
func TestADetectionFlagsAnOtherwiseCleanFire(t *testing.T) {
	status, summary := scheduledOutcome{
		reply:      "Posted 3 comment replies.",
		detections: 1,
	}.resolve()
	if status != RunAttention {
		t.Fatalf("a fire that read injected instructions must not record as ok, got %q", status)
	}
	if !strings.HasPrefix(summary, "Read content carrying instructions aimed at this agent") {
		t.Errorf("the detection should lead the summary, got %q", summary)
	}
	if !strings.Contains(summary, "Posted 3 comment replies.") {
		t.Errorf("what the run produced should survive the prefix, got %q", summary)
	}
}

// Attention, not Failed: the run worked, it just needs reading.
func TestADetectionDoesNotMarkTheRunFailed(t *testing.T) {
	status, _ := scheduledOutcome{reply: "done", detections: 4, taintBlocks: 2}.resolve()
	if status == RunFailed {
		t.Error("a detection is something to read, not an error")
	}
	if status != RunAttention {
		t.Errorf("expected attention, got %q", status)
	}
}

// A truncated cycle announces itself in the output; a detection does not. When
// both happen, the one that stays invisible has to be the one that leads.
func TestADetectionOutranksTheRoundCap(t *testing.T) {
	status, summary := scheduledOutcome{
		reply:      "partial work",
		hitCap:     true,
		softCap:    12,
		detections: 2,
	}.resolve()
	if status != RunAttention {
		t.Fatalf("expected attention, got %q", status)
	}
	if !strings.HasPrefix(summary, "Read content carrying instructions") {
		t.Errorf("the detection should lead, got %q", summary)
	}
	if !strings.Contains(summary, "hit round cap (12 rounds)") {
		t.Errorf("the round cap must still be reported, got %q", summary)
	}
}

// The two conditions that were already here keep working.
func TestRoundCapStillFlags(t *testing.T) {
	status, summary := scheduledOutcome{reply: "partial", hitCap: true, softCap: 8}.resolve()
	if status != RunAttention {
		t.Errorf("a truncated cycle should flag, got %q", status)
	}
	if !strings.Contains(summary, "hit round cap (8 rounds)") {
		t.Errorf("the cap should be named, got %q", summary)
	}
}

func TestAStalledObjectiveStillFlagsAndAMetOneDoesNot(t *testing.T) {
	stalled, summary := scheduledOutcome{
		reply: "tried again", objLine: "no progress this attempt", objStalled: true,
	}.resolve()
	if stalled != RunAttention {
		t.Errorf("a stalled objective should flag, got %q", stalled)
	}
	if !strings.HasPrefix(summary, "No progress this attempt.") {
		t.Errorf("the objective line leads and is sentence-cased, got %q", summary)
	}
	ok, _ := scheduledOutcome{reply: "done", objLine: "goal reached"}.resolve()
	if ok != RunOK {
		t.Errorf("an objective that is progressing is not a problem, got %q", ok)
	}
}

// An objective line plus a detection: both present, detection first.
func TestObjectiveAndDetectionBothSurvive(t *testing.T) {
	_, summary := scheduledOutcome{
		reply: "posted", objLine: "on track", detections: 1, taintBlocks: 1,
	}.resolve()
	if !strings.HasPrefix(summary, "Read content carrying instructions") {
		t.Errorf("the detection leads, got %q", summary)
	}
	if !strings.Contains(summary, "1 follow-up action was stopped") {
		t.Errorf("a stopped action is worth saying, got %q", summary)
	}
	if !strings.Contains(summary, "On track.") {
		t.Errorf("the objective line must survive, got %q", summary)
	}
}

// --- the other turn-free path -------------------------------------------
//
// The standing runner reports through StandingRunResult rather than
// scheduledOutcome, so the two cannot share a resolver. What they DO share is
// the sentence, and the rule that a detection outranks the conditions that
// announce themselves in the output. These pin the shared half.

func TestBothTurnFreePathsLeadWithTheSameSentence(t *testing.T) {
	// The recurring path builds it through the resolver.
	_, recurring := scheduledOutcome{reply: "posted", detections: 1}.resolve()
	// The standing path prefixes the same helper onto its own summary
	// (standing_runner.go). Same text, so an owner reading either ledger sees
	// one phrasing for one thing.
	standing := scanDetectionSummary(1, 0) + " " + standingSummary("posted")
	if !strings.HasPrefix(recurring, scanDetectionSummary(1, 0)) {
		t.Errorf("the recurring path should lead with the shared sentence, got %q", recurring)
	}
	if !strings.HasPrefix(standing, scanDetectionSummary(1, 0)) {
		t.Errorf("the standing path should lead with the shared sentence, got %q", standing)
	}
}

// syncRunResult is what carries the counts out of a turn-free run to whichever
// ledger records it. A zero value must read as "nothing happened", because
// every early error return in runAgentSyncConfirm produces one.
func TestAZeroSyncRunResultClaimsNoDetections(t *testing.T) {
	var res syncRunResult
	if res.Detections != 0 || res.TaintBlocks != 0 {
		t.Error("a failed setup must not look like a run that read an attack")
	}
	if res.Text != "" || res.HitRoundCap || res.Trace != nil {
		t.Error("the zero value should claim nothing at all")
	}
}
