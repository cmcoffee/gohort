package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The whole "secured" guarantee — access only through declaring tools — rests on
// the fetch_url auto-route skipping secured credentials. If a covered host could
// still auto-route through a secured cred, the secret would be reachable without
// a declaring tool, defeating the lock.
func TestSecuredCredentialSkipsAutoRoute(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	url := "https://api.example.com/v1/call"

	// Secured: skipped entirely — the host reads as not credential-covered.
	s.db.Set(secureAPITable, "sec", SecureCredential{Name: "sec", BaseURL: "https://api.example.com", Secured: true})
	if name, err := s.AutoRouteCredential(url); name != "" || err != nil {
		t.Fatalf("secured cred must be skipped by auto-route: name=%q err=%v", name, err)
	}

	// The SAME credential, not secured, IS seen as covering (it errors here only
	// because no secret is set) — proving it was the Secured flag, not the URL,
	// that excluded it above.
	s.db.Set(secureAPITable, "sec", SecureCredential{Name: "sec", BaseURL: "https://api.example.com", Secured: false})
	if _, err := s.AutoRouteCredential(url); err == nil {
		t.Fatal("non-secured cred covering the host should be seen (expected a covered-but-no-secret error)")
	}
}

// TestAutoRouteMatchesInternalHost pins the invariant the handleFetch reorder
// depends on: AutoRouteCredential matches purely on BaseURL prefix and has NO
// public/private notion, so a credential whose BaseURL is an INTERNAL host
// (.local / private IP) still covers it. This is why moving the auto-route
// ahead of the non-public-host SSRF refusal in the sandbox fetch hook lets a
// script reach a self-hosted, credential-scoped API — the exact case
// (ts3_api on teamspeak.snuglab.local) that broke when its host got scoped.
func TestAutoRouteMatchesInternalHost(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	// Non-secured, enabled, internal .local BaseURL, secret set.
	s.db.Set(secureAPITable, "ts3_api", SecureCredential{Name: "ts3_api", BaseURL: "http://teamspeak.snuglab.local:10080"})
	s.db.Set(secureAPITable, secureCredSecretKey("ts3_api"), "tok")

	name, err := s.AutoRouteCredential("http://teamspeak.snuglab.local:10080/clientlist")
	if err != nil {
		t.Fatalf("auto-route errored on a covered internal host: %v", err)
	}
	if name != "ts3_api" {
		t.Fatalf("internal covered host must auto-route via its credential; got name=%q (a nil/empty result is the pre-reorder bug: SSRF guard would then refuse it)", name)
	}

	// A private-IP BaseURL is likewise covered — no public-only carve-out.
	s.db.Set(secureAPITable, "lan_api", SecureCredential{Name: "lan_api", BaseURL: "http://10.0.0.5:8080"})
	s.db.Set(secureAPITable, secureCredSecretKey("lan_api"), "tok")
	if name, err := s.AutoRouteCredential("http://10.0.0.5:8080/status"); name != "lan_api" || err != nil {
		t.Fatalf("private-IP covered host must auto-route; name=%q err=%v", name, err)
	}

	// An UNCOVERED internal host stays unmatched → falls through to the SSRF
	// refusal in the fetch hook, exactly as intended.
	if name, err := s.AutoRouteCredential("http://other.internal:9000/x"); name != "" || err != nil {
		t.Fatalf("uncovered internal host must NOT auto-route (must reach the SSRF guard); name=%q err=%v", name, err)
	}
}

// TestSecuredToolBindingLifecycle covers the two-state binding model: approve
// (auto-bind / un-revoke) ↔ revoke (durable deny), with idempotence and the
// approve/revoke mutual exclusion.
func TestSecuredToolBindingLifecycle(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	s.db.Set(secureAPITable, "ts3_api", SecureCredential{Name: "ts3_api", Secured: true})

	if s.ToolBindingApproved("ts3_api", "ts3_status") || s.ToolBindingRevoked("ts3_api", "ts3_status") {
		t.Fatal("no binding state initially")
	}

	// Approve → bound; idempotent.
	if err := s.ApproveToolBinding("ts3_api", "ts3_status"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	_ = s.ApproveToolBinding("ts3_api", "ts3_status")
	c, _ := s.Load("ts3_api")
	if len(c.ApprovedToolBindings) != 1 {
		t.Fatalf("approve must be idempotent; approved=%v", c.ApprovedToolBindings)
	}
	if !s.ToolBindingApproved("ts3_api", "ts3_status") {
		t.Fatal("should be bound after approve")
	}

	// Revoke → tombstoned, no longer approved (mutual exclusion).
	if err := s.RevokeToolBinding("ts3_api", "ts3_status"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s.ToolBindingApproved("ts3_api", "ts3_status") || !s.ToolBindingRevoked("ts3_api", "ts3_status") {
		t.Fatal("revoke must move approved → revoked")
	}

	// Approve again → un-revokes.
	if err := s.ApproveToolBinding("ts3_api", "ts3_status"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if !s.ToolBindingApproved("ts3_api", "ts3_status") || s.ToolBindingRevoked("ts3_api", "ts3_status") {
		t.Fatal("re-approve must un-revoke")
	}
}

// TestEnforceSecuredBinding covers P1 slice 2 dispatch enforcement: open creds
// and unnamed callers pass; a legacy declaring tool is grandfathered on first
// dispatch; a revoked binding is refused (tombstone survives, not re-
// grandfathered); a re-approval un-revokes.
func TestEnforceSecuredBinding(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	s.db.Set(secureAPITable, "sec", SecureCredential{Name: "sec", Secured: true})
	s.db.Set(secureAPITable, "open", SecureCredential{Name: "open"})

	if err := s.EnforceSecuredBinding("open", "any_tool", ""); err != nil {
		t.Fatalf("open cred must always pass: %v", err)
	}
	if err := s.EnforceSecuredBinding("sec", "", ""); err != nil {
		t.Fatalf("unnamed caller must pass: %v", err)
	}

	// Legacy declaring tool → grandfathered (allowed) + recorded approved.
	if err := s.EnforceSecuredBinding("sec", "legacy", ""); err != nil {
		t.Fatalf("legacy declaring tool must be grandfathered: %v", err)
	}
	if !s.ToolBindingApproved("sec", "legacy") {
		t.Fatal("grandfather must record the binding as approved")
	}

	// Revoke → tombstoned → refused, and NOT re-grandfathered on the next call.
	if err := s.RevokeToolBinding("sec", "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnforceSecuredBinding("sec", "legacy", ""); err == nil {
		t.Fatal("revoked binding must be refused")
	}
	if s.ToolBindingApproved("sec", "legacy") {
		t.Fatal("a revoked binding must not be silently re-approved by enforcement")
	}

	// Re-approve → un-revoked → passes again.
	if err := s.ApproveToolBinding("sec", "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnforceSecuredBinding("sec", "legacy", ""); err != nil {
		t.Fatalf("re-approved binding must pass: %v", err)
	}
}

// TestCredentialDispatchAccessByKind pins the by-kind access gate: an OPEN cred is
// gated by its user ACL (WHO) — enforced at dispatch so fetch_via / api-mode can't
// reach a cred the user wasn't shared — while a SECURED cred DEFERS to its bound
// tools (WHAT) and does NOT consult AllowedUsers (a stale/hidden list must not gate
// it). The two models never compose.
func TestCredentialDispatchAccessByKind(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}

	// SECURED cred that (still) carries a leftover AllowedUsers from before it was
	// secured — it must be IGNORED; access follows the binding only.
	s.db.Set(secureAPITable, "sec", SecureCredential{
		Name: "sec", Secured: true, AllowedUsers: []string{"alice"},
		ApprovedToolBindings: []string{"bound"},
	})
	// A user OUTSIDE the leftover AllowedUsers, dispatching the bound tool → PASSES:
	// a secured cred defers to whoever has the tool, not the user list.
	if err := s.EnforceSecuredBinding("sec", "bound", "bob"); err != nil {
		t.Fatalf("a secured cred must defer to its binding, ignoring AllowedUsers: %v", err)
	}
	// The WHAT axis still applies: a revoked binding is refused regardless of user.
	if err := s.RevokeToolBinding("sec", "bound"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnforceSecuredBinding("sec", "bound", "alice"); err == nil {
		t.Fatal("a revoked binding must be refused even for a listed user")
	}

	// OPEN credential with an allowlist: the WHO axis applies at dispatch, so a
	// tool's fetch_via / api-mode dispatch can't reach it for a non-grantee.
	s.db.Set(secureAPITable, "open_restricted", SecureCredential{
		Name: "open_restricted", AllowedUsers: []string{"alice"},
	})
	if err := s.EnforceSecuredBinding("open_restricted", "some_tool", "alice"); err != nil {
		t.Fatalf("alice is granted the open cred: %v", err)
	}
	if err := s.EnforceSecuredBinding("open_restricted", "some_tool", "bob"); err == nil {
		t.Fatal("an OPEN cred with an allowlist must refuse a non-grantee at dispatch (fetch_via WHO gap)")
	}

	// UserMayUse directly: open, allowlisted, owned.
	if !s.UserMayUse(SecureCredential{}, "anyone") {
		t.Fatal("an open (empty AllowedUsers) cred admits everyone")
	}
	if s.UserMayUse(SecureCredential{AllowedUsers: []string{"alice"}}, "bob") {
		t.Fatal("an allowlisted cred must exclude a non-member")
	}
	if !s.UserMayUse(SecureCredential{Owner: "carol"}, "carol") || s.UserMayUse(SecureCredential{Owner: "carol"}, "dave") {
		t.Fatal("a user-owned cred admits only its owner")
	}
}

// TestSecuredBindingForgetKeepsRevoke covers the auto-resolve model's durable
// deny: a REVOKE tombstone survives ForgetToolBinding (the delete path), so an
// admin's deny persists across a delete + same-name recreate. A forget on an
// APPROVED (not revoked) tool clears it cleanly.
func TestSecuredBindingForgetKeepsRevoke(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	s.db.Set(secureAPITable, "sec", SecureCredential{Name: "sec", Secured: true})

	// Approved tool → forget clears it entirely (no revoke to preserve).
	s.ApproveToolBinding("sec", "a")
	if err := s.ForgetToolBinding("sec", "a"); err != nil {
		t.Fatal(err)
	}
	if s.ToolBindingApproved("sec", "a") || s.ToolBindingRevoked("sec", "a") {
		t.Fatal("forget on an approved tool must leave it in no list")
	}

	// Revoked tool → forget KEEPS the tombstone (durable deny survives delete).
	s.RevokeToolBinding("sec", "r")
	if err := s.ForgetToolBinding("sec", "r"); err != nil {
		t.Fatal(err)
	}
	if !s.ToolBindingRevoked("sec", "r") {
		t.Fatal("forget must PRESERVE a revoke tombstone so the deny survives delete")
	}
	// And a "recreate" (dispatch after forget) is still refused.
	if err := s.EnforceSecuredBinding("sec", "r", ""); err == nil {
		t.Fatal("a forgotten-but-revoked binding must still be refused at dispatch")
	}
}

func TestSetSecuredRoundTrip(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	s.db.Set(secureAPITable, "c", SecureCredential{Name: "c"})

	if err := s.SetSecured("c", true); err != nil {
		t.Fatal(err)
	}
	var on SecureCredential // fresh struct per read — gob omits false, so a reused
	s.db.Get(secureAPITable, "c", &on)
	if !on.Secured {
		t.Fatal("SetSecured(true) did not persist")
	}
	if err := s.SetSecured("c", false); err != nil {
		t.Fatal(err)
	}
	var off SecureCredential // target would keep a stale true
	s.db.Get(secureAPITable, "c", &off)
	if off.Secured {
		t.Fatal("SetSecured(false) did not clear")
	}
}
