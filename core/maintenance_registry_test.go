package core

// Every maintenance action the deployment ships must actually be in the
// registry after package init.
//
// This exists because one silently wasn't. The source-hook engine moved to a
// leaf package and kept registering its cache sweep through a hook that core's
// init assigns — but a leaf initializes BEFORE the package importing it, so the
// hook was still nil, the nil-guard returned quietly, and the action
// disappeared from the admin surface. Nothing failed; it was simply gone.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShippedMaintenanceActionsAreRegistered(t *testing.T) {
	want := map[string]bool{
		"sweep_expired_caches":         false,
		"survey_workspace_bound_tools": false,
		"survey_workspace_usage":       false,
		"survey_reapable_artifacts":    false,
		"reap_workspace_artifacts":     false,
	}
	for _, m := range ListMaintenanceFuncs() {
		if _, ok := want[m.Key]; ok {
			want[m.Key] = true
		}
		if m.Label == "" || m.Desc == "" {
			t.Errorf("maintenance %q has no label or description — the admin surface renders both", m.Key)
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("maintenance action %q is not registered; if it moved to a leaf package, register it from core — a leaf's init runs first and a hook assigned in core's init is still nil there", key)
		}
	}
}

// A page opened mid-run found an idle row, and a second press ran the same
// pass alongside the first. Most passes never report progress, so "running"
// could not be read off the progress line: it has to be a fact of its own, and
// the second start has to wait on the first rather than walk the store again.
func TestAMaintenancePassRunsOnceAndIsVisibleWhileItRuns(t *testing.T) {
	saved := maintenanceFuncs
	t.Cleanup(func() { maintenanceFuncs = saved })

	const key = "test_silent_pass"
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	RegisterMaintenanceFunc("Housekeeping", key, "Silent pass", "Says nothing while it runs.",
		func(ctx context.Context) int {
			mu.Lock()
			calls++
			mu.Unlock()
			<-release
			return 7
		})

	results := make(chan int, 2)
	go func() { results <- RunMaintenanceFunc(context.Background(), key) }()

	// Silent, and still visibly running: that is what a page arriving now
	// rejoins on.
	deadline := time.Now().Add(2 * time.Second)
	for MaintenanceProgress(key) == "" {
		if time.Now().After(deadline) {
			t.Fatal("a running pass that has said nothing reads as not running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := MaintenanceProgress(key); !strings.HasPrefix(got, "running") {
		t.Errorf("a silent running pass reads %q", got)
	}

	// The second press, while the first is in flight.
	go func() { results <- RunMaintenanceFunc(context.Background(), key) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case n := <-results:
			if n != 7 {
				t.Errorf("a start returned %d, want the pass's own 7", n)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a start never returned")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("the pass ran %d times for two overlapping starts", calls)
	}
	if got := MaintenanceProgress(key); got != "" {
		t.Errorf("a finished pass still reads as running: %q", got)
	}
	if got := MaintenanceOutcome(key); !strings.Contains(got, "7 record(s)") {
		t.Errorf("the outcome is %q", got)
	}
}

// A pass that panics must still release its key, or the row reads as running
// for the life of the process and every later press waits forever.
func TestAPanickingMaintenancePassReleasesItsKey(t *testing.T) {
	saved := maintenanceFuncs
	t.Cleanup(func() { maintenanceFuncs = saved })

	const key = "test_panicking_pass"
	RegisterMaintenanceFunc("Housekeeping", key, "Panicking pass", "Falls over.",
		func(ctx context.Context) int { panic("boom") })
	func() {
		defer func() { _ = recover() }()
		RunMaintenanceFunc(context.Background(), key)
	}()
	if got := MaintenanceProgress(key); got != "" {
		t.Errorf("a pass that panicked still reads as running: %q", got)
	}
	if got := MaintenanceOutcome(key); got != "" {
		t.Errorf("a pass that panicked claimed an outcome: %q", got)
	}
}
