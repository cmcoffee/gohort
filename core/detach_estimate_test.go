package core

// The framework asked for a number and then punished the reply for containing
// it.
//
// detachedNotice says "This usually takes about 13 seconds; say so if it is
// worth knowing." The model said so. The turn judge's machinery rule excuses "a
// duration the assistant was given" — but nothing ever told the judge one HAD
// been given, so the exception was dead text, the estimate read as invented,
// and the loop retracted a message the framework itself had specified. One
// wasted round, one retracted bubble, every detach with a measured duration.

import (
	"strings"
	"testing"
	"time"
)

func TestTheOfferedWaitIsRecordedForTheJudge(t *testing.T) {
	sess := &ToolSession{}
	if got := sess.detachLedger().EstimateText(); got != "" {
		t.Errorf("a turn that offered no wait reports %q, want empty", got)
	}

	sess.RecordDetachEstimate(13 * time.Second)
	got := sess.detachLedger().EstimateText()
	if got == "" {
		t.Fatal("the offered wait was not recorded — the judge cannot tell it from an invented one")
	}
	// It must be the WORDS the model saw, since the judge compares against a
	// sentence the model wrote.
	if want := humanizeTaskDuration(13 * time.Second); got != want {
		t.Errorf("estimate text = %q, want the phrasing the model was given, %q", got, want)
	}
	if !strings.Contains(detachedNotice(TaskRun{ID: "t1"}, 13*time.Second), got) {
		t.Errorf("the notice offers a different wording than the judge is told about (%q)", got)
	}
}

func TestZeroAndLongestWaitHandling(t *testing.T) {
	sess := &ToolSession{}
	// A tool with no measured duration must not register one: the notice tells
	// the model to put NO time on it, so there is nothing to excuse.
	sess.RecordDetachEstimate(0)
	if got := sess.detachLedger().EstimateText(); got != "" {
		t.Errorf("a zero duration was recorded as %q — the notice told the model to quote nothing", got)
	}
	// Two jobs: the honest number is the one that keeps the user waiting.
	sess.RecordDetachEstimate(10 * time.Second)
	sess.RecordDetachEstimate(4 * time.Minute)
	sess.RecordDetachEstimate(30 * time.Second)
	if got, want := sess.detachLedger().EstimateText(), humanizeTaskDuration(4*time.Minute); got != want {
		t.Errorf("with several jobs running the estimate is %q, want the longest (%q)", got, want)
	}
}

// A nil ledger is the no-detach case and must answer quietly rather than panic
// on the judge's path.
func TestNilLedgerEstimateIsSafe(t *testing.T) {
	var d *DetachLedger
	if got := d.EstimateText(); got != "" {
		t.Errorf("nil ledger returned %q", got)
	}
}
