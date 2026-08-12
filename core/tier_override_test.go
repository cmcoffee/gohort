package core

import (
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestTierOverridePrecedence — narrowest wins. An explicit per-run override
// beats the route stage, which beats the config's own Tier. That order is what
// lets one resource opt out of a deployment-wide routing decision without the
// stage having to know the resource exists.
func TestTierOverridePrecedence(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })

	RegisterRouteStage(RouteStage{Key: "tiertest.open", Label: "Open", Default: "worker"})
	prevLookup := LookupRouteFunc
	LookupRouteFunc = func(key string) string {
		var v string
		db.Get(RoutingTable, key, &v)
		return v
	}
	t.Cleanup(func() { LookupRouteFunc = prevLookup })
	db.Set(RoutingTable, "tiertest.open", "worker")

	// No override: the stage decides.
	cfg := AgentLoopConfig{RouteKey: "tiertest.open", Tier: LEAD}
	if cfg.wantsLead() {
		t.Error("the route stage said worker and was overruled by cfg.Tier")
	}
	// An override beats the stage, in both directions.
	cfg.TierOverride = LEAD
	if !cfg.wantsLead() {
		t.Error("a LEAD override did not beat a worker route stage")
	}
	db.Set(RoutingTable, "tiertest.open", "lead")
	cfg.TierOverride = WORKER
	if cfg.wantsLead() {
		t.Error("a WORKER override did not beat a lead route stage — a resource cannot " +
			"opt out of an escalation it should never get")
	}
	// TierUnset is transparent: unchanged behavior for every existing caller.
	cfg.TierOverride = TierUnset
	if !cfg.wantsLead() {
		t.Error("TierUnset stopped following the route stage")
	}
	// With no route key at all, cfg.Tier is the answer.
	if !(AgentLoopConfig{Tier: LEAD}).wantsLead() {
		t.Error("a bare LEAD config does not want lead")
	}
	if (AgentLoopConfig{}).wantsLead() {
		t.Error("an empty config wants lead")
	}
}

// TestAnOverrideCannotDefeatThePrivacyPin — THE rule. A per-resource "always
// lead" flag is exactly the kind of thing that quietly becomes the exception to
// a security pin, so this pins that it does not.
//
// wantsLead answers only "what was configured"; every caller gates it on
// LeadDenied(). The composition is what matters, so it is asserted here rather
// than trusted to three separate call sites.
func TestAnOverrideCannotDefeatThePrivacyPin(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })

	var app AppCore
	app.Private() // servitor's posture

	cfg := AgentLoopConfig{TierOverride: LEAD}
	if !cfg.wantsLead() {
		t.Fatal("the override does not read as wanting lead")
	}
	// Privacy off (the default): the pin binds and the override is inert.
	if !app.LeadDenied() {
		t.Fatal("a private AppCore does not deny the lead with privacy off")
	}
	if !app.LeadDenied() && cfg.wantsLead() {
		t.Error("an override reached the lead tier with privacy off")
	}
	// Privacy on: the same override now takes effect.
	SetAllLLMsPrivate(true)
	if app.LeadDenied() {
		t.Error("the pin still binds with every model private, so the override can never apply")
	}
	if !(!app.LeadDenied() && cfg.wantsLead()) {
		t.Error("with every model private the override did not reach the lead tier")
	}
}

// TestEveryTierDecisionIsPrivacyGated — a source sweep, because the property is
// structural.
//
// wantsLead answers "what was configured" and knows nothing about privacy; the
// pin lives in LeadDenied() at each call site. Removing that guard from a site
// is a one-token edit that no behavioural test in this package caught — it was
// mutation-tested and passed, which is how this test came to exist. Each site
// is individually plausible and only a rule covering all of them sees the gap.
func TestEveryTierDecisionIsPrivacyGated(t *testing.T) {
	src, err := os.ReadFile("agent_loop.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	found := 0
	for i, line := range lines {
		if !strings.Contains(line, "cfg.wantsLead()") {
			continue
		}
		// The declaration itself is the definition, not a use.
		if strings.Contains(line, "func ") {
			continue
		}
		found++
		guarded := false
		for back := i; back >= 0 && back > i-4; back-- {
			if strings.Contains(lines[back], "LeadDenied()") {
				guarded = true
				break
			}
		}
		if !guarded {
			t.Errorf("agent_loop.go:%d decides the tier without LeadDenied() in front of it:\n  %s\n"+
				"An appliance pinned to the lead would then escalate with Model Privacy off, "+
				"putting SSH credentials and log contents on a third-party model.", i+1, strings.TrimSpace(line))
		}
	}
	if found == 0 {
		t.Fatal("found no tier decisions — the sweep is no longer looking where they live")
	}
}
