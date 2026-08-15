package core

// The two cache lifetimes are an economic choice, not a preference, so
// the tests are about the things that would silently cost money: the
// default not moving, the beta being declared only when used, and the
// cost model following the setting.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// withLongCache points the tunable store at a scratch DB and sets the
// knob, restoring both afterwards.
func withLongCache(t *testing.T, on bool) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	if on {
		db.Set(WebTable, "tune_prompt_cache_1h", float64(1))
	}
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(nil) })
}

func TestDefaultCacheLifetimeIsUnchanged(t *testing.T) {
	withLongCache(t, false)
	cc := ephemeralCache()
	if cc.TTL != "" {
		t.Errorf("the default must stay the 5-minute cache — changing what a deployment is billed on upgrade is not a silent act. Got ttl=%q", cc.TTL)
	}
	// And nothing declares a beta it is not using.
	if betas := promptCacheBetas(); len(betas) != 0 {
		t.Errorf("beta declared while the feature is off: %v", betas)
	}
	// The write multiplier is the 5-minute figure.
	if got := (CostRates{}).EffectiveCacheWriteMultiplier(); got != 1.25 {
		t.Errorf("write multiplier = %v, want 1.25 for the 5-minute cache", got)
	}
}

func TestLongCacheStampsTTLAndDeclaresTheBeta(t *testing.T) {
	withLongCache(t, true)
	cc := ephemeralCache()
	if cc.TTL != "1h" {
		t.Fatalf("ttl = %q, want 1h", cc.TTL)
	}
	if cc.Type != "ephemeral" {
		t.Errorf("type should stay ephemeral: %q", cc.Type)
	}
	// Serialized shape matters — the API reads this, not the struct.
	raw, _ := json.Marshal(cc)
	if !strings.Contains(string(raw), `"ttl":"1h"`) {
		t.Errorf("ttl missing from the wire form: %s", raw)
	}
	if betas := promptCacheBetas(); len(betas) != 1 || betas[0] != extendedCacheTTL {
		t.Errorf("beta not declared for the body-carried path: %v", betas)
	}
	// The cost model must follow, or a deployment that just started
	// writing 2x caches keeps reporting them at 1.25x.
	if got := (CostRates{}).EffectiveCacheWriteMultiplier(); got != 2.0 {
		t.Errorf("write multiplier = %v, want 2.0 under the 1-hour cache", got)
	}
	// An explicit operator setting still wins over the automatic one.
	if got := (CostRates{CacheWriteMultiplier: 1.4}).EffectiveCacheWriteMultiplier(); got != 1.4 {
		t.Errorf("an explicit multiplier must not be overridden: %v", got)
	}
}

// Every breakpoint gets the same lifetime — a mixed set writes part of
// the prefix short-lived and re-primes it while the rest is still warm.
func TestAllBreakpointsShareTheLifetime(t *testing.T) {
	withLongCache(t, true)
	tools := buildAnthTools([]Tool{{Name: "a"}, {Name: "b"}})
	if len(tools) == 0 || tools[len(tools)-1].CacheControl == nil {
		t.Fatal("no breakpoint on the tools block")
	}
	if tools[len(tools)-1].CacheControl.TTL != "1h" {
		t.Error("the tools breakpoint kept the short lifetime")
	}
	sys := buildSystemBlocks("SYSTEM")
	if len(sys) == 0 || sys[0].CacheControl == nil || sys[0].CacheControl.TTL != "1h" {
		t.Error("the system breakpoint kept the short lifetime")
	}
}
