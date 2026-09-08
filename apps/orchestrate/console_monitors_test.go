package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestTheRowMarkSaysWHYNotJustTHAT. Four things stop a monitor and only one of
// them is anybody's problem; a single "stopped" mark would put the broken one
// and the finished one on the same footing, which is the state the bare Paused
// bool already left the UI in.
func TestTheRowMarkSaysWHYNotJustTHAT(t *testing.T) {
	cases := []struct {
		name     string
		monitor  EventMonitor
		wantIcon string
		wantTone string
		wantSays string
	}{
		{"running", EventMonitor{Name: "live"}, "", "", ""},
		{"owner paused", EventMonitor{Name: "m", Paused: true, StopReason: MonitorStopOwner}, "pause", "muted", "paused"},
		{"spent its fires", EventMonitor{Name: "m", Paused: true, StopReason: MonitorStopFinished}, "check", "muted", "fired the number of times"},
		{"condition met", EventMonitor{Name: "m", Paused: true, StopReason: MonitorStopMet}, "check", "muted", "what it was watching for happened"},
		{"went quiet", EventMonitor{Name: "m", Paused: true, StopReason: MonitorStopIdle}, "off", "muted", "stopped watching"},
		{"broken", EventMonitor{Name: "m", Paused: true, Broken: true, StopReason: MonitorStopBroken, BrokenReason: "no such host"}, "alert", "warn", "no such host"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := monitorRowState(c.monitor)
			if c.wantIcon == "" {
				if st != nil {
					t.Fatalf("a running monitor got a mark: %v", st)
				}
				return
			}
			if st == nil {
				t.Fatal("a stopped monitor got no mark")
			}
			if st["icon"] != c.wantIcon || st["tone"] != c.wantTone {
				t.Errorf("icon=%v tone=%v, want %s/%s", st["icon"], st["tone"], c.wantIcon, c.wantTone)
			}
			title, _ := st["title"].(string)
			if !strings.Contains(title, c.wantSays) {
				t.Errorf("the tooltip does not explain itself: %q", title)
			}
		})
	}

	// The one that must never be muted: an owner scanning a rail is looking
	// for the row that needs them.
	if st := monitorRowState(EventMonitor{Name: "m", Paused: true, Broken: true}); st["tone"] != "warn" {
		t.Errorf("a broken monitor reads as tone %v — it is the only one that needs the owner", st["tone"])
	}
}

// TestALegacyPausedMonitorStillReadsSensibly: records written before
// StopReason existed carry an empty one, and must not render as a blank or a
// wrong state.
func TestALegacyPausedMonitorStillReadsSensibly(t *testing.T) {
	if got := (EventMonitor{Paused: true}).StopCause(); got != MonitorStopOwner {
		t.Errorf("a legacy paused monitor reads as %q, want owner-paused — the only thing that could pause one back then", got)
	}
	if got := (EventMonitor{Paused: true, Broken: true}).StopCause(); got != MonitorStopBroken {
		t.Errorf("a legacy broken monitor reads as %q", got)
	}
	// Spent is derivable, so a legacy capped monitor reads as finished rather
	// than as something the owner did.
	spent := EventMonitor{Paused: true, MaxFires: 2, FireCount: 2}
	if got := spent.StopCause(); got != MonitorStopFinished {
		t.Errorf("a legacy spent monitor reads as %q, want finished", got)
	}
	// And a running one is not at rest at all.
	if got := (EventMonitor{}).StopCause(); got != "" {
		t.Errorf("a running monitor has a stop cause %q", got)
	}
}

// TestAChannelIsMarkedOnlyByWhatDeliversIntoIt. A monitor that wakes an agent
// in a thread has nothing to do with a channel that agent happens to be bound
// to; marking the channel for it would say something untrue about a
// conversation.
func TestAChannelIsMarkedOnlyByWhatDeliversIntoIt(t *testing.T) {
	ch := Channel{ID: "ch1", Address: "any;+;chat99", AgentID: "agent-1"}

	elsewhere := EventMonitor{Name: "wakes-the-agent", Paused: true, Broken: true, WakeAgent: "agent-1"}
	if st := channelRowState(ch, []EventMonitor{elsewhere}); st != nil {
		t.Errorf("a monitor that only wakes the bound agent marked the channel: %v", st)
	}

	bound := EventMonitor{Name: "feeds-the-channel", Paused: true, StopReason: MonitorStopFinished, WakeChannel: "ch1"}
	if st := channelRowState(ch, []EventMonitor{elsewhere, bound}); st == nil || st["icon"] != "check" {
		t.Errorf("a stopped monitor bound to the channel did not mark it: %v", st)
	}

	// A direct-delivery monitor posts into the conversation itself.
	direct := EventMonitor{Name: "posts-here", Paused: true, StopReason: MonitorStopIdle, DeliverChatID: "any;+;chat99"}
	if st := channelRowState(ch, []EventMonitor{direct}); st == nil || st["icon"] != "off" {
		t.Errorf("a direct-delivery monitor did not mark its own conversation: %v", st)
	}

	// Several feeders: the one worth acting on wins, not the newest or the
	// first — a channel with one broken watcher and three finished ones has a
	// problem, and the problem is what the row should say.
	broken := EventMonitor{Name: "broken", Paused: true, Broken: true, WakeChannel: "ch1", BrokenReason: "no such host"}
	st := channelRowState(ch, []EventMonitor{bound, direct, broken})
	if st == nil || st["tone"] != "warn" {
		t.Errorf("the urgent feeder lost to a finished one: %v", st)
	}

	// Nothing stopped, no mark: most rows, most of the time.
	running := EventMonitor{Name: "fine", WakeChannel: "ch1"}
	if st := channelRowState(ch, []EventMonitor{running}); st != nil {
		t.Errorf("a healthy channel was marked: %v", st)
	}
}

// TestAMetObjectiveDoesNotReadAsAPause. The runner used to stop a schedule
// that reached its goal by setting Paused and nothing else, so the one outcome
// an objective exists to produce rendered exactly like a schedule somebody
// paused by hand. The reason lived in the run ledger and the log — neither of
// which is where you look when scanning a list of schedules.
func TestAMetObjectiveDoesNotReadAsAPause(t *testing.T) {
	met := StandingAgent{Name: "blog", Paused: true, StopCause: StoppedByMet, StopNote: "the post is live and linked"}
	lbl := scheduleStopLabel(StandingStopCause(met), StandingStopNote(met))
	if !strings.Contains(lbl, "done") {
		t.Errorf("a met objective reads as %q", lbl)
	}
	if !strings.Contains(lbl, "the post is live") {
		t.Errorf("the checker's reason is not shown: %q", lbl)
	}
	if lbl == "paused" {
		t.Error("a met objective is still indistinguishable from a manual pause")
	}

	// A person's pause still reads as exactly that, and says nothing more.
	if got := scheduleStopLabel(StandingStopCause(StandingAgent{Paused: true, StopCause: StoppedByOwner}), ""); got != "paused" {
		t.Errorf("an owner pause reads as %q", got)
	}
	// A schedule written before the cause existed reads as an owner pause,
	// which is what a paused schedule used to mean.
	if got := StandingStopCause(StandingAgent{Paused: true}); got != StoppedByOwner {
		t.Errorf("a legacy paused schedule reads as %q", got)
	}
	// And a running one is not stopped at all.
	if got := scheduleStopLabel(StandingStopCause(StandingAgent{}), ""); got != "" {
		t.Errorf("a running schedule shows the state %q", got)
	}
}

// TestOnlyTheStopsThatNeedAPersonAreAmber is what makes the rail's attention
// count mean anything: it counts warn-toned marks, so every state that tones
// warn is a claim on someone's time.
func TestOnlyTheStopsThatNeedAPersonAreAmber(t *testing.T) {
	cases := []struct {
		cause    string
		wantIcon string
		wantTone string
	}{
		{StoppedByMet, "check", "muted"},
		{StoppedByOwner, "pause", "muted"},
		{ParkedByObjective, "alert", "warn"},
		{ParkedByDependency, "alert", "warn"},
	}
	for _, c := range cases {
		st := scheduleRowState("nightly", c.cause, "because")
		if st == nil {
			t.Fatalf("%s produced no mark", c.cause)
		}
		if st["icon"] != c.wantIcon || st["tone"] != c.wantTone {
			t.Errorf("%s → %v/%v, want %s/%s", c.cause, st["icon"], st["tone"], c.wantIcon, c.wantTone)
		}
		title, _ := st["title"].(string)
		if !strings.HasPrefix(title, "nightly — ") {
			t.Errorf("%s: the tooltip does not name the schedule: %q", c.cause, title)
		}
		// The glyph symbols belong in the glyph, not in the hover text.
		if strings.ContainsAny(title, "⚠✓") {
			t.Errorf("%s: the tooltip repeats the mark: %q", c.cause, title)
		}
	}
	// A running schedule gets no mark, so it cannot be counted as needing
	// anything.
	if st := scheduleRowState("nightly", "", ""); st != nil {
		t.Errorf("a running schedule was marked: %v", st)
	}
}
