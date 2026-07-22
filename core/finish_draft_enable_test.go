package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestFinishDraftAutoEnables locks the "not clear I had to enable it" fix: a
// draft credential lands DISABLED with a placeholder secret, and Save preserves
// Disabled — so the My-API-credentials save handler must AUTO-ENABLE when the
// user provides the real secret to finish a pending draft. This exercises the
// exact primitive sequence the handler runs (status → Save → conditional
// SetDisabledOwned), and the negative case (an already-enabled edit is not
// touched).
func TestFinishDraftAutoEnables(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}

	draft := SecureCredential{Name: "cal", Type: SecureCredBasicAuth,
		BaseURL: "https://p188-caldav.icloud.com", Owner: "alice"}
	if err := s.SaveAPIDraft(draft); err != nil {
		t.Fatalf("draft: %v", err)
	}
	// Pending draft: disabled, no real secret.
	_, enabled, hasSecret := s.CredentialStatusOwned("alice", "cal")
	if enabled || hasSecret {
		t.Fatalf("fresh draft should be disabled + secretless; enabled=%v hasSecret=%v", enabled, hasSecret)
	}

	// Handler sequence: snapshot status, Save the secret, auto-enable iff it was
	// a pending draft and a secret was provided.
	_, wasEnabled, hadSecret := s.CredentialStatusOwned("alice", "cal")
	if err := s.Save(draft, "alice@example.com:pw"); err != nil {
		t.Fatalf("save secret: %v", err)
	}
	if !wasEnabled && !hadSecret {
		if err := s.SetDisabledOwned("alice", "cal", false); err != nil {
			t.Fatalf("enable: %v", err)
		}
	}
	_, enabled, hasSecret = s.CredentialStatusOwned("alice", "cal")
	if !enabled || !hasSecret {
		t.Fatalf("finishing a draft should leave it enabled + with a secret; enabled=%v hasSecret=%v", enabled, hasSecret)
	}

	// Negative: editing config on an ALREADY-enabled cred (blank secret) must
	// not be seen as finishing a draft, so it stays exactly as-is.
	_, wasEnabled, hadSecret = s.CredentialStatusOwned("alice", "cal")
	edited := draft
	edited.BaseURL = "https://p188-caldav.icloud.com/195178399"
	if err := s.Save(edited, ""); err != nil {
		t.Fatalf("edit config: %v", err)
	}
	autoEnabled := !wasEnabled && !hadSecret // false — it was live
	if autoEnabled {
		t.Fatal("an already-enabled edit must not trigger auto-enable logic")
	}
	c, _ := s.LoadUser("alice", "cal")
	if c.Disabled || c.BaseURL != "https://p188-caldav.icloud.com/195178399" {
		t.Fatalf("config edit should apply and stay enabled; disabled=%v base=%q", c.Disabled, c.BaseURL)
	}
}
