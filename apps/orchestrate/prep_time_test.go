package orchestrate

import (
	"strings"
	"testing"
	"time"
)

// The clock reports once. The loop calls a model many times per turn and only
// the first one has a person waiting on it.
func TestPrepClockReportsOnce(t *testing.T) {
	p := startPrepClock()
	p.mark("recall", 4229*time.Millisecond)
	p.done()
	if !p.logged {
		t.Fatal("first done() did not report")
	}
	before := len(p.marks)
	p.done()
	if len(p.marks) != before {
		t.Fatal("a second done() changed the record")
	}
}

// A nil clock is the normal case off the live send path — a fire, a dispatch, a
// test. Every method has to tolerate it or each caller grows a branch.
func TestNilPrepClockIsSafe(t *testing.T) {
	var p *prepClock
	p.mark("recall", time.Second)
	p.done()
}

// The remainder is the point: what nobody measured shows up as the gap between
// the total and the parts, which is where the next slow phase will be.
func TestUnmeasuredTimeIsReportedNotHidden(t *testing.T) {
	p := &prepClock{received: time.Now().Add(-3 * time.Second)}
	p.mark("recall", 100*time.Millisecond)
	var line string
	p.render(func(s string) { line = s })
	if !strings.Contains(line, "recall 100ms") {
		t.Fatalf("named phase missing: %s", line)
	}
	if !strings.Contains(line, "other 2.9s") {
		t.Fatalf("unmeasured time not surfaced: %s", line)
	}
}

// Phases can run concurrently and outrun the wall clock. That must not print a
// negative remainder.
func TestConcurrentPhasesDoNotGoNegative(t *testing.T) {
	p := &prepClock{received: time.Now().Add(-100 * time.Millisecond)}
	p.mark("a", time.Second)
	p.mark("b", time.Second)
	var line string
	p.render(func(s string) { line = s })
	if strings.Contains(line, "-") {
		t.Fatalf("negative remainder: %s", line)
	}
	if !strings.Contains(line, "other 0s") {
		t.Fatalf("remainder should floor at zero: %s", line)
	}
}
