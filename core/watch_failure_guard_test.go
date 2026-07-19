package core

import (
	"context"
	"errors"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestWatchPollFailed(t *testing.T) {
	cases := []struct {
		name string
		body string
		err  error
		want bool
	}{
		{"clean output", "SouthPawn has left TeamSpeak.", nil, false},
		{"tool error", "", errors.New("boom"), true},
		{"nonzero exit", "partial\n[exit: exit status 1]", nil, true},
		{"timeout", "[TIMED OUT after 1m30s — command killed.]", nil, true},
		{"python traceback", "Traceback (most recent call last):\n  File x\ngohort.HookError: fetch refused", nil, true},
	}
	for _, c := range cases {
		if got, _ := watchPollFailed(c.body, c.err); got != c.want {
			t.Errorf("%s: watchPollFailed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWatchFailureCircuitBreaker: K consecutive failed polls mark the monitor
// broken + paused (no delivery), and a success before K resets the streak.
func TestWatchFailureCircuitBreaker(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const traceback = "Traceback (most recent call last):\ngohort.HookError: binding revoked\n[exit: exit status 1]"

	result := traceback
	RegisterWatchToolInvoker(func(owner, agentID, toolName string, args map[string]any) (string, error) {
		return result, nil
	})
	defer RegisterWatchToolInvoker(nil)

	m := EventMonitor{Owner: "u", Name: "w", Kind: EventKindWatch, ToolName: "ts3_status", Notify: EventNotifyDirect}
	SaveEventMonitor(db, m)

	// Two failures — not yet broken.
	executeWatchPoll(context.Background(), db, m)
	executeWatchPoll(context.Background(), db, m)
	if got, _ := GetEventMonitor(db, "u", "w"); got.Broken {
		t.Fatalf("must not be broken before the threshold (failures=%d)", got.ConsecutiveFailures)
	}

	// A success resets the streak.
	result = "SouthPawn has left TeamSpeak."
	executeWatchPoll(context.Background(), db, m)
	if got, _ := GetEventMonitor(db, "u", "w"); got.ConsecutiveFailures != 0 || got.Broken {
		t.Fatalf("a successful poll must reset the streak; failures=%d broken=%v", got.ConsecutiveFailures, got.Broken)
	}

	// Now K straight failures → broken + paused.
	result = traceback
	for i := 0; i < watchFailureThreshold; i++ {
		executeWatchPoll(context.Background(), db, m)
	}
	got, _ := GetEventMonitor(db, "u", "w")
	if !got.Broken || !got.Paused {
		t.Fatalf("after %d consecutive failures the monitor must be broken+paused; broken=%v paused=%v failures=%d",
			watchFailureThreshold, got.Broken, got.Paused, got.ConsecutiveFailures)
	}
}
