// Tests for the pacing tool. The one property everything else hangs off is that
// what the model is TOLD is what the host will act on.
package pacing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core"
)

// The one property everything else hangs off: what the model is TOLD is what the
// host will act on. A confirmation that echoed the request rather than the
// result would teach the model that a time it never got was honoured, and it
// would go on asking for it.
func mustAsk(t *testing.T, tool core.AgentToolDef, args map[string]any) string {
	t.Helper()
	out, err := tool.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("%s(%v): %v", tool.Tool.Name, args, err)
	}
	return out
}

func pacingFixture(spec ToolSpec) (core.AgentToolDef, *Ask, time.Time) {
	start := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	ask := &Ask{}
	spec.Ask = ask
	spec.Now = func() time.Time { return start }
	if spec.Loc == nil {
		spec.Loc = time.UTC
	}
	return Tool(spec), ask, start
}

func TestAskRecordsWhatItConfirms(t *testing.T) {
	tool, ask, start := pacingFixture(ToolSpec{Min: 5 * time.Minute, Max: 24 * time.Hour})

	out := mustAsk(t, tool, map[string]any{"minutes": 90, "why": "the build is still running"})
	at, why, ok := ask.Get()
	if !ok {
		t.Fatal("the ask was not recorded")
	}
	if want := start.Add(90 * time.Minute); !at.Equal(want) {
		t.Fatalf("recorded %s, wanted %s", at, want)
	}
	if why != "the build is still running" {
		t.Fatalf("reason not recorded verbatim: %q", why)
	}
	if !strings.Contains(out, at.Format("15:04")) {
		t.Fatalf("the confirmation does not name the time that was recorded: %q", out)
	}
	if !strings.Contains(out, "still counts") {
		t.Errorf("the confirmation must say the attempt still counts, or deferring reads as a way out of the bound: %q", out)
	}
}

// Clamps report themselves. A silently moved time is the same failure as a
// wrong one: the model asked for something, got something else, and was told it
// got what it asked for.
func TestClampsSayThemselves(t *testing.T) {
	tool, ask, start := pacingFixture(ToolSpec{
		Min: 30 * time.Minute, Max: 6 * time.Hour,
		MaxReason: "this task is dropped after 14 idle day(s)",
	})

	out := mustAsk(t, tool, map[string]any{"minutes": 1, "why": "retry shortly"})
	at, _, _ := ask.Get()
	if want := start.Add(30 * time.Minute); !at.Equal(want) {
		t.Fatalf("a sub-minimum ask was not raised to the floor: got %s, wanted %s", at, want)
	}
	if !strings.Contains(out, "minimum") {
		t.Errorf("the floor was applied silently: %q", out)
	}

	out = mustAsk(t, tool, map[string]any{"minutes": 60 * 24 * 3, "why": "waiting on the vendor"})
	at, _, _ = ask.Get()
	if want := start.Add(6 * time.Hour); !at.Equal(want) {
		t.Fatalf("an over-ceiling ask was not lowered: got %s, wanted %s", at, want)
	}
	if !strings.Contains(out, "idle day") {
		t.Errorf("the ceiling's REASON is the useful half and is missing: %q", out)
	}
	if !strings.Contains(out, at.Format("15:04")) {
		t.Fatalf("the confirmation names a time other than the one recorded: %q", out)
	}
}

// The host's own shaping runs last and still has to be reported. For a
// recurring task that is the active window: pacing buys a different hour, never
// an hour the owner said no to.
func TestTheHostsAdjustmentIsReported(t *testing.T) {
	var adjusted time.Time
	tool, ask, start := pacingFixture(ToolSpec{
		Min: time.Minute, Max: 24 * time.Hour,
		Adjust: func(at time.Time) time.Time {
			adjusted = at.Add(3 * time.Hour) // stand-in for "the next window open"
			return adjusted
		},
	})

	out := mustAsk(t, tool, map[string]any{"minutes": 60, "why": "waiting for the queue to drain"})
	at, _, _ := ask.Get()
	if !at.Equal(adjusted) {
		t.Fatalf("the host's adjustment was not applied: recorded %s, adjusted %s", at, adjusted)
	}
	if want := start.Add(4 * time.Hour); !at.Equal(want) {
		t.Fatalf("recorded %s, wanted %s", at, want)
	}
	if !strings.Contains(out, "allowed to run") {
		t.Errorf("a deferral into the window must be reported: %q", out)
	}
}

func TestAnAskNeedsAReasonAndATime(t *testing.T) {
	tool, ask, _ := pacingFixture(ToolSpec{Min: time.Minute, Max: time.Hour})

	if _, err := tool.Handler(context.Background(), map[string]any{"minutes": 30}); err == nil {
		t.Error("an ask with no reason was accepted; the reason is what the owner reads")
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"why": "waiting"}); err == nil {
		t.Error("an ask with neither minutes nor at was accepted")
	}
	if _, _, ok := ask.Get(); ok {
		t.Error("a refused ask was recorded anyway")
	}
}

func TestALocalClockTimeIsRead(t *testing.T) {
	tool, ask, start := pacingFixture(ToolSpec{Min: time.Minute, Max: 48 * time.Hour})

	mustAsk(t, tool, map[string]any{"at": "2026-09-14 16:30", "why": "after the standup"})
	at, _, _ := ask.Get()
	if want := time.Date(2026, 9, 14, 16, 30, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("local clock time read as %s, wanted %s", at, want)
	}

	// A time already gone is a request that cannot be honoured, and answering it
	// with "moved to yesterday" would be worse than refusing.
	if _, err := tool.Handler(context.Background(), map[string]any{"at": "2026-09-14 09:00", "why": "earlier"}); err == nil {
		t.Error("a time in the past was accepted")
	}
	if at2, _, _ := ask.Get(); !at2.Equal(at) {
		t.Error("the refused ask overwrote the good one")
	}
	_ = start
}

// Nothing is gained by letting a turn negotiate with itself, so the last ask
// wins. The count exists so the host can say that in the log rather than let
// the earlier asks vanish.
func TestLastAskWins(t *testing.T) {
	tool, ask, start := pacingFixture(ToolSpec{Min: time.Minute, Max: 24 * time.Hour})

	mustAsk(t, tool, map[string]any{"minutes": 30, "why": "first thought"})
	mustAsk(t, tool, map[string]any{"minutes": 120, "why": "actually the deploy is queued"})

	at, why, _ := ask.Get()
	if want := start.Add(120 * time.Minute); !at.Equal(want) {
		t.Fatalf("the last ask did not win: got %s, wanted %s", at, want)
	}
	if why != "actually the deploy is queued" {
		t.Fatalf("reason not from the last ask: %q", why)
	}
	if ask.Count() != 2 {
		t.Fatalf("asks counted %d, wanted 2", ask.Count())
	}
}

// A host that never mounted the tool still reads the ask, and gets "no".
func TestNilAskIsSafe(t *testing.T) {
	var ask *Ask
	if _, _, ok := ask.Get(); ok {
		t.Error("a nil ask reported an ask")
	}
	if ask.Count() != 0 {
		t.Error("a nil ask counted an ask")
	}
}
