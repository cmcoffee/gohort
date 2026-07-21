package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

type fakeSub struct {
	ensureCalls, deleteCalls int
	exp                      time.Time
	fail                     bool
}

func (f *fakeSub) Ensure(string) (time.Time, error) {
	f.ensureCalls++
	if f.fail {
		return time.Time{}, fmt.Errorf("boom")
	}
	return f.exp, nil
}

func (f *fakeSub) Delete(string) error { f.deleteCalls++; return nil }

func TestPushSubLifecycle(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })

	f := &fakeSub{exp: time.Now().Add(time.Hour)}
	RegisterPushSubHandler("test_kind", f)

	// Ensure creates + persists.
	if err := EnsurePushSubscription("test_kind", "k1", 20*time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.ensureCalls != 1 {
		t.Fatalf("ensure calls = %d, want 1", f.ensureCalls)
	}
	var r pushSubRecord
	if !RootDB.Get(pushSubTable, pushSubID("test_kind", "k1"), &r) || r.Key != "k1" {
		t.Fatalf("record not persisted: %+v", r)
	}

	// Not near expiry → sweep is a no-op.
	sweepPushSubs()
	if f.ensureCalls != 1 {
		t.Errorf("sweep renewed early (calls=%d)", f.ensureCalls)
	}

	// Within the renewBefore window → sweep renews.
	r.ExpiresAt = time.Now().Add(5 * time.Minute)
	RootDB.Set(pushSubTable, pushSubID("test_kind", "k1"), r)
	sweepPushSubs()
	if f.ensureCalls != 2 {
		t.Errorf("sweep did not renew (calls=%d)", f.ensureCalls)
	}

	// Remove → provider delete + record gone.
	RemovePushSubscription("test_kind", "k1")
	if f.deleteCalls != 1 {
		t.Errorf("delete calls = %d, want 1", f.deleteCalls)
	}
	var gone pushSubRecord
	if RootDB.Get(pushSubTable, pushSubID("test_kind", "k1"), &gone) {
		t.Error("record not removed")
	}

	// Unknown kind → error, nothing persisted.
	if err := EnsurePushSubscription("no_such_kind", "x", time.Minute); err == nil {
		t.Error("expected error for unregistered kind")
	}
}
