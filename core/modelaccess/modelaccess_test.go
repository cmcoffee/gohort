package modelaccess

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func withStore(t *testing.T) {
	t.Helper()
	prev := core.AuthDB
	db := &core.DBase{Store: kvlite.MemStore()}
	core.AuthDB = func() core.Database { return db }
	t.Cleanup(func() { core.AuthDB = prev })
}

// Unset limits are the defaults; a limit is never zero, which would read as
// unlimited.
func TestLimitsAreNeverUnlimited(t *testing.T) {
	withStore(t)
	if l := Load(); l.PerMinute != DefaultPerMinute || l.AtOnce != DefaultAtOnce {
		t.Errorf("unset limits: %+v", l)
	}
	for _, bad := range []Limits{{0, 2}, {30, 0}, {-1, 2}} {
		if Save(bad) == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if err := Save(Limits{PerMinute: 5, AtOnce: 3}); err != nil || Load() != (Limits{PerMinute: 5, AtOnce: 3}) {
		t.Errorf("saved limits read back as %+v (%v)", Load(), err)
	}
}

// Each person has their own allowance, the refusal says which limit, and a
// changed limit applies at once.
func TestEachPersonIsHeldToTheLimits(t *testing.T) {
	withStore(t)
	Save(Limits{PerMinute: 2, AtOnce: 5})
	for i := 0; i < 2; i++ {
		done, why := Admit("ann")
		if why != "" {
			t.Fatalf("request %d refused: %s", i+1, why)
		}
		done()
	}
	if _, why := Admit("ann"); !strings.Contains(why, "this minute") {
		t.Errorf("past the per-minute limit: %q", why)
	}
	if _, why := Admit("ben"); why != "" {
		t.Errorf("ann's use held ben: %s", why)
	}
	Save(Limits{PerMinute: 100, AtOnce: 1})
	done, why := Admit("ann")
	if why != "" {
		t.Fatalf("a raised limit did not apply at once: %s", why)
	}
	if _, why := Admit("ann"); !strings.Contains(why, "already running") {
		t.Errorf("past the at-once limit: %q", why)
	}
	done()
}
