package core

import "testing"

func TestTokenScopeGrandfatherAndDeny(t *testing.T) {
	// Legacy key (nil Scope) — minted before scoping — must stay unrestricted,
	// or turning enforcement on breaks a live integration.
	legacy := &AccountToken{Owner: "u"}
	if !legacy.IsLegacyUnscoped() {
		t.Fatal("nil-scope token should read as legacy-unscoped")
	}
	if !legacy.AllowsFeature("openai") || !legacy.AllowsTarget("agent:x") {
		t.Error("legacy key must be unrestricted")
	}

	// A scoped key is deny-by-default: only what's listed passes.
	scoped := &AccountToken{Owner: "u", Scope: &TokenScope{
		Features: []string{"openai"},
		Targets:  []string{"worker", "agent:abc"},
	}}
	if scoped.IsLegacyUnscoped() {
		t.Error("a scoped key is not legacy")
	}
	if !scoped.AllowsFeature("openai") || scoped.AllowsFeature("webhook") {
		t.Error("feature allow-list wrong")
	}
	if !scoped.AllowsTarget("worker") || !scoped.AllowsTarget("agent:abc") {
		t.Error("listed targets should pass")
	}
	if scoped.AllowsTarget("agent:other") || scoped.AllowsTarget("channel:z") {
		t.Error("unlisted target must be denied")
	}

	// An empty (non-nil) scope denies everything — the deny-by-default new key.
	empty := &AccountToken{Owner: "u", Scope: &TokenScope{}}
	if empty.IsLegacyUnscoped() {
		t.Error("empty scope is explicit, not legacy")
	}
	if empty.AllowsFeature("openai") || empty.AllowsTarget("worker") {
		t.Error("empty scope must deny all")
	}

	// Matching is case/space-insensitive so "Worker" or " worker " don't leak.
	sp := &AccountToken{Scope: &TokenScope{Targets: []string{"Worker"}}}
	if !sp.AllowsTarget(" worker ") {
		t.Error("target match should fold case + trim space")
	}

	// nil receiver denies (never panics).
	var nilTok *AccountToken
	if nilTok.AllowsFeature("openai") || nilTok.AllowsTarget("worker") {
		t.Error("nil token must deny")
	}
}
