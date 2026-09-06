package core

import (
	"github.com/cmcoffee/snugforge/kvlite"
	"strings"
	"testing"
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

// A global credential and a user-owned credential of the SAME NAME must keep
// separate audit ledgers. Before the owner-aware keying, both wrote to the bare
// name, so one user's dispatch history showed another namespace's calls and the
// daily-cap counter counted across users.
func TestAuditRingIsPerOwner(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}

	s.recordAudit(SecureAPIAuditEntry{CredentialName: "vapi", Owner: "", URL: "https://global", Status: 200})
	s.recordAudit(SecureAPIAuditEntry{CredentialName: "vapi", Owner: "alice", URL: "https://alice", Status: 200})
	s.recordAudit(SecureAPIAuditEntry{CredentialName: "vapi", Owner: "alice", URL: "https://alice2", Status: 200})

	global := s.LoadAudit("", "vapi")
	if len(global) != 1 || global[0].URL != "https://global" {
		t.Fatalf("global ledger leaked or lost entries: %+v", global)
	}
	alice := s.LoadAudit("alice", "vapi")
	if len(alice) != 2 {
		t.Fatalf("alice's ledger should hold exactly her 2 calls, got %d", len(alice))
	}
	for _, e := range alice {
		if e.URL == "https://global" {
			t.Error("alice's ledger contains a global-namespace call — cross-user leak")
		}
	}
	// The global read must not see alice's calls either.
	for _, e := range global {
		if e.Owner == "alice" {
			t.Error("global ledger contains alice's call — cross-user leak")
		}
	}
}

// Global credentials (Owner "") keep the bare-name key, so ledgers recorded
// before the owner-aware change remain readable — no history is orphaned.
func TestAuditGlobalKeyUnchanged(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	// Simulate a pre-change entry written under the bare name.
	s.db.Set(secureAPIAuditTable, "legacy", []SecureAPIAuditEntry{{CredentialName: "legacy", URL: "https://old"}})
	got := s.LoadAudit("", "legacy")
	if len(got) != 1 || got[0].URL != "https://old" {
		t.Fatalf("pre-change global ledger not readable: %+v", got)
	}
}

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

func securedFixture(t *testing.T) *SecureAPI {
	t.Helper()
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	if err := s.Save(SecureCredential{
		Name: "gitlab", Type: SecureCredBearer, CredScope: "per_user",
		BaseURL: "https://gitlab.example.com",
	}, "placeholder"); err != nil {
		t.Fatal(err)
	}
	return s
}

// On a per_user credential the secret being spent is the USER'S. Whether every
// agent they have gets a generic call_<name> route to spend it is a decision
// about their key — and it used to be one admin switch governing everyone's.
func TestAUserCanLockTheirOwnPerUserCredential(t *testing.T) {
	s := securedFixture(t)
	c, ok := s.Load("gitlab")
	if !ok {
		t.Fatal("fixture credential missing")
	}
	if s.EffectiveSecured(c, "alice") {
		t.Fatal("nobody has locked anything yet")
	}
	if err := s.SetUserSecured("gitlab", "alice", true); err != nil {
		t.Fatal(err)
	}
	if !s.EffectiveSecured(c, "alice") {
		t.Error("alice locked her own key and it did not take")
	}
	// One user's lock is theirs alone — it is about their secret, and bob has
	// a different one.
	if s.EffectiveSecured(c, "bob") {
		t.Error("alice's lock reached bob's key")
	}
	// And it comes back off, leaving no row behind.
	if err := s.SetUserSecured("gitlab", "alice", false); err != nil {
		t.Fatal(err)
	}
	if s.EffectiveSecured(c, "alice") {
		t.Error("unlocking did not take")
	}
}

// OR, never a replacement. A user may tighten what the admin left open; they
// may not lift a lock the admin set, because that one is about the deployment's
// exposure rather than their key.
func TestAnAdminLockCannotBeLiftedByAUser(t *testing.T) {
	s := securedFixture(t)
	if err := s.SetSecured("gitlab", true); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Load("gitlab")
	if !s.EffectiveSecured(c, "alice") {
		t.Fatal("the admin's lock must apply to every user")
	}
	// Even with the user's own flag explicitly off.
	if err := s.SetUserSecured("gitlab", "alice", false); err != nil {
		t.Fatal(err)
	}
	if !s.EffectiveSecured(c, "alice") {
		t.Error("a user unset their own flag and the admin's lock came off with it")
	}
}

// Every other kind reads exactly as it did. A shared credential has no per-user
// secret to protect, and a user-owned one already carries the mode on its own
// record — so a stray per-user flag on one must change nothing.
func TestOnlyPerUserCredentialsGainTheUserLock(t *testing.T) {
	s := securedFixture(t)
	if err := s.Save(SecureCredential{
		Name: "shared_api", Type: SecureCredBearer,
		BaseURL: "https://api.example.com",
	}, "sekrit"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserSecured("shared_api", "alice", true); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Load("shared_api")
	if s.EffectiveSecured(c, "alice") {
		t.Error("a shared credential must not take a per-user lock — the secret is not the user's")
	}
	// And no user at all (catalog enumeration outside a request) reads the
	// admin's setting alone, which can only ever be the narrower answer.
	perUser, _ := s.Load("gitlab")
	_ = s.SetUserSecured("gitlab", "alice", true)
	if s.EffectiveSecured(perUser, "") {
		t.Error("a userless enumeration must not inherit somebody's lock")
	}
}

// TestCredentialUserNamespaceStore covers the (owner, name)-keyed store: user-
// owned creds are isolated per user (no name collisions), List() stays global,
// ListUser/LoadUser scope to a user, Resolve shadows global with the user's own,
// secrets are per-namespace, and DeleteUser only touches that user's cred.
func TestCredentialUserNamespaceStore(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	mk := func(name, owner, secret string) {
		c := SecureCredential{Name: name, Owner: owner, Type: SecureCredBearer, BaseURL: "https://x.test"}
		if err := s.Save(c, secret); err != nil {
			t.Fatalf("save %s/%s: %v", owner, name, err)
		}
	}
	mk("shared", "", "gk")      // global
	mk("github", "alice", "ak") // alice's — same name as bob's, must not collide
	mk("github", "bob", "bk")

	names := func(cs []SecureCredential) map[string]string {
		m := map[string]string{}
		for _, c := range cs {
			m[c.Name] = c.Owner
		}
		return m
	}

	// List() is the GLOBAL namespace only.
	g := names(s.List())
	if _, ok := g["github"]; ok || g["shared"] != "" {
		t.Fatalf("List() must be global-only, no owners; got %v", g)
	}
	if _, ok := g["shared"]; !ok {
		t.Fatalf("List() must include the global cred; got %v", g)
	}

	// ListUser scopes to a user.
	if au := s.ListUser("alice"); len(au) != 1 || au[0].Name != "github" || au[0].Owner != "alice" {
		t.Fatalf("ListUser(alice); got %v", au)
	}

	// Two users' same-named creds are distinct records.
	ac, aok := s.LoadUser("alice", "github")
	bc, bok := s.LoadUser("bob", "github")
	if !aok || !bok || ac.Owner != "alice" || bc.Owner != "bob" {
		t.Fatalf("user creds collided; a=%v(%v) b=%v(%v)", ac, aok, bc, bok)
	}

	// Resolve: the user's own shadows a global of the same name; falls through to
	// global; misses when neither exists.
	if c, ok := s.Resolve("github", "alice"); !ok || c.Owner != "alice" {
		t.Fatalf("resolve(github, alice) must be alice's; got %v %v", c, ok)
	}
	if c, ok := s.Resolve("shared", "alice"); !ok || c.Owner != "" {
		t.Fatalf("resolve(shared, alice) must fall through to global; got %v %v", c, ok)
	}
	if _, ok := s.Resolve("github", "carol"); ok {
		t.Fatal("resolve must miss when neither a user nor global cred exists")
	}

	// resolveSecret (the dispatch path) loads a user-owned cred's secret from its
	// owner-namespaced key.
	if s2, ok := s.resolveSecret(bc, "bob"); !ok || s2 != "bk" {
		t.Fatalf("resolveSecret for a user-owned cred; got %q %v", s2, ok)
	}

	// Secrets are per-namespace.
	var sec string
	if !s.db.Get(secureAPITable, secureCredSecretKey(credStoreKey("alice", "github")), &sec) || sec != "ak" {
		t.Fatalf("alice's secret keyed wrong; got %q", sec)
	}
	if !s.db.Get(secureAPITable, secureCredSecretKey(credStoreKey("bob", "github")), &sec) || sec != "bk" {
		t.Fatalf("bob's secret keyed wrong; got %q", sec)
	}

	// DeleteUser removes only that user's cred + secret.
	if err := s.DeleteUser("alice", "github"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LoadUser("alice", "github"); ok {
		t.Fatal("DeleteUser didn't remove the cred")
	}
	if s.db.Get(secureAPITable, secureCredSecretKey(credStoreKey("alice", "github")), &sec) {
		t.Fatal("DeleteUser left the secret behind")
	}
	if _, ok := s.LoadUser("bob", "github"); !ok {
		t.Fatal("DeleteUser hit the wrong user")
	}
}

// TestUserOwnedGovernance covers the admin governance helpers over the user
// plane: ListAllUserOwned enumerates every user's own creds (never global), and
// SetDisabledOwned revokes a user-owned cred in its owner's namespace without
// touching a same-named cred of another user or the global one.
func TestUserOwnedGovernance(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	mk := func(name, owner string) {
		if err := s.Save(SecureCredential{Name: name, Owner: owner, Type: SecureCredBearer, BaseURL: "https://x.test"}, "sek"); err != nil {
			t.Fatalf("save %s/%s: %v", owner, name, err)
		}
	}
	mk("shared", "") // global — must NOT appear in ListAllUserOwned
	mk("github", "alice")
	mk("github", "bob") // same name, different owner
	mk("jira", "alice")

	owned := s.ListAllUserOwned()
	if len(owned) != 3 {
		t.Fatalf("ListAllUserOwned must return the 3 user-owned creds (not the global); got %d: %v", len(owned), owned)
	}
	for _, c := range owned {
		if c.Owner == "" {
			t.Fatalf("ListAllUserOwned must exclude global creds; got %v", c)
		}
	}

	// SetDisabledOwned revokes only alice's github — not bob's, not the global.
	if err := s.SetDisabledOwned("alice", "github", true); err != nil {
		t.Fatal(err)
	}
	ac, _ := s.LoadUser("alice", "github")
	bc, _ := s.LoadUser("bob", "github")
	gc, _ := s.Load("shared")
	if !ac.Disabled {
		t.Fatal("alice's github must be disabled")
	}
	if bc.Disabled {
		t.Fatal("bob's same-named github must be untouched")
	}
	if gc.Disabled {
		t.Fatal("the global cred must be untouched")
	}

	// Re-enable clears it.
	if err := s.SetDisabledOwned("alice", "github", false); err != nil {
		t.Fatal(err)
	}
	if ac, _ := s.LoadUser("alice", "github"); ac.Disabled {
		t.Fatal("re-enable must clear disabled")
	}
}

// TestBuildToolsUserNamespace covers the auto-catalog: BuildTools surfaces the
// session user's OWN credentials as fetch_url_<name> tools, a nil session sees
// global-only, and a user cred shadows a same-named global (one tool, not two).
func TestBuildToolsUserNamespace(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	save := func(name, owner, secret string) {
		if err := s.Save(SecureCredential{Name: name, Owner: owner, Type: SecureCredBearer, BaseURL: "https://x.test"}, secret); err != nil {
			t.Fatalf("save %s/%s: %v", owner, name, err)
		}
	}
	save("shared", "", "gk")    // global-only
	save("dup", "", "gk")       // global, shadowed by alice's
	save("dup", "alice", "ak")  // alice's own, same name as the global
	save("mine", "alice", "ak") // alice-only

	names := func(sess *ToolSession) (map[string]bool, int) {
		m := map[string]bool{}
		dup := 0
		for _, td := range s.BuildTools(sess) {
			m[td.Tool.Name] = true
			if td.Tool.Name == "fetch_url_dup" {
				dup++
			}
		}
		return m, dup
	}

	// Nil session (headless) → global namespace only.
	g, _ := names(nil)
	if !g["fetch_url_shared"] || g["fetch_url_mine"] {
		t.Fatalf("nil-session BuildTools must be global-only; got %v", g)
	}
	// Alice's session → her own + global; fetch_url_dup appears exactly once.
	a, dup := names(&ToolSession{Username: "alice"})
	if !a["fetch_url_mine"] || !a["fetch_url_shared"] || !a["fetch_url_dup"] {
		t.Fatalf("alice must see her own + global tools; got %v", a)
	}
	if dup != 1 {
		t.Fatalf("fetch_url_dup must appear once (user shadows global); got %d", dup)
	}
	// Bob's session → global only; alice's creds are invisible to him.
	b, _ := names(&ToolSession{Username: "bob"})
	if b["fetch_url_mine"] || !b["fetch_url_shared"] {
		t.Fatalf("bob must not see alice's creds; got %v", b)
	}
}

// A user-owned credential could not be secured at all: SetSecured reads the
// GLOBAL key and a user's credential lives at credStoreKey(owner, name). So
// every credential someone managed themselves was Open, and an Open credential
// gets an auto-generated fetch_url_<name> — a person's own API key reachable by
// every agent they had.

func securedStore(t *testing.T) *SecureAPI {
	t.Helper()
	return &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
}

// TestSecuringAUserOwnedCredential — the gap itself.
func TestSecuringAUserOwnedCredential(t *testing.T) {
	s := securedStore(t)
	if err := s.Save(SecureCredential{Name: "mykey", Type: SecureCredBearer,
		BaseURL: "https://api.example.com", Owner: "craig"}, "sk-1"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if c, _ := s.LoadUser("craig", "mykey"); c.Secured {
		t.Fatal("a new credential is secured by default — every existing record would change meaning")
	}
	if err := s.SetSecuredOwned("craig", "mykey", true); err != nil {
		t.Fatalf("secure: %v", err)
	}
	c, ok := s.LoadUser("craig", "mykey")
	if !ok || !c.Secured {
		t.Fatal("the user-owned credential was not secured")
	}
	// And back off again — a one-way door would be its own bug.
	if err := s.SetSecuredOwned("craig", "mykey", false); err != nil {
		t.Fatalf("unsecure: %v", err)
	}
	if c, _ := s.LoadUser("craig", "mykey"); c.Secured {
		t.Error("securing could not be undone")
	}
}

// TestSecuringNeverCrossesNamespaces — the reason a separate setter exists. A
// global credential of the same name must not be touched by a user securing
// theirs, and vice versa.
func TestSecuringNeverCrossesNamespaces(t *testing.T) {
	s := securedStore(t)
	s.Save(SecureCredential{Name: "shared", Type: SecureCredBearer, BaseURL: "https://x.example"}, "g")
	s.Save(SecureCredential{Name: "shared", Type: SecureCredBearer, BaseURL: "https://x.example", Owner: "craig"}, "u")

	if err := s.SetSecuredOwned("craig", "shared", true); err != nil {
		t.Fatalf("secure owned: %v", err)
	}
	if g, _ := s.Load("shared"); g.Secured {
		t.Error("securing a user's credential secured the GLOBAL one of the same name")
	}
	if u, _ := s.LoadUser("craig", "shared"); !u.Secured {
		t.Error("the user's own credential was not secured")
	}
	// Another user's namespace is untouched and unreachable.
	if err := s.SetSecuredOwned("bob", "shared", true); err == nil {
		t.Error("securing succeeded for a user who owns no such credential")
	}
}

// TestSavePreservesSecured — Save deliberately keeps the flag from the stored
// record, so editing a credential without mentioning it cannot silently reopen
// one that was locked down.
func TestSavePreservesSecured(t *testing.T) {
	s := securedStore(t)
	s.Save(SecureCredential{Name: "k", Type: SecureCredBearer, BaseURL: "https://a.example", Owner: "craig"}, "s")
	s.SetSecuredOwned("craig", "k", true)

	// An ordinary edit — a new description, no Secured field set at all.
	if err := s.Save(SecureCredential{Name: "k", Type: SecureCredBearer,
		BaseURL: "https://a.example", Description: "edited", Owner: "craig"}, ""); err != nil {
		t.Fatalf("edit: %v", err)
	}
	c, _ := s.LoadUser("craig", "k")
	if !c.Secured {
		t.Error("an edit reopened a secured credential to every agent")
	}
	if c.Description != "edited" {
		t.Errorf("the edit did not land: %q", c.Description)
	}
}

// TestABlankOwnerFallsThroughToGlobal — so callers need no branch.
func TestABlankOwnerFallsThroughToGlobal(t *testing.T) {
	s := securedStore(t)
	s.Save(SecureCredential{Name: "g", Type: SecureCredBearer, BaseURL: "https://a.example"}, "s")
	if err := s.SetSecuredOwned("", "g", true); err != nil {
		t.Fatalf("global via owned setter: %v", err)
	}
	if c, _ := s.Load("g"); !c.Secured {
		t.Error("a blank owner did not reach the global credential")
	}
	if err := s.SetSecuredOwned("craig", "nope", true); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Errorf("securing a missing credential should say so: %v", err)
	}
}

// TestSecuringRemovesTheAutoGeneratedTool — the behaviour the toggle exists for,
// asserted through the catalog rather than the flag.
//
// A stored bool is not the promise. The promise is that no agent gets a
// fetch_url_<name> for this credential any more, and that is decided in
// BuildTools — so this drives BuildTools and would catch the flag being set
// correctly while the catalog kept emitting the tool anyway.
func TestSecuringRemovesTheAutoGeneratedTool(t *testing.T) {
	s := securedStore(t)
	if err := s.Save(SecureCredential{Name: "mykey", Type: SecureCredBearer,
		BaseURL: "https://api.example.com", Owner: "craig"}, "sk-1"); err != nil {
		t.Fatalf("save: %v", err)
	}
	sess := &ToolSession{Username: "craig"}

	if !catalogHasTool(s, sess, "fetch_url_mykey") {
		t.Fatal("an OPEN user credential produced no tool — the baseline this test compares against is wrong")
	}
	if err := s.SetSecuredOwned("craig", "mykey", true); err != nil {
		t.Fatalf("secure: %v", err)
	}
	if catalogHasTool(s, sess, "fetch_url_mykey") {
		t.Error("the credential is secured but every agent still gets its tool — " +
			"the toggle changed a flag and nothing else")
	}
	// Reopening restores it, so the control is not a one-way door.
	s.SetSecuredOwned("craig", "mykey", false)
	if !catalogHasTool(s, sess, "fetch_url_mykey") {
		t.Error("reopening the credential did not restore its tool")
	}
}

// TestSecuringOneCredentialLeavesTheOthers — a lock is only useful if it is
// narrow. Securing one must not quietly remove the rest from the catalog.
func TestSecuringOneCredentialLeavesTheOthers(t *testing.T) {
	s := securedStore(t)
	s.Save(SecureCredential{Name: "locked", Type: SecureCredBearer, BaseURL: "https://a.example", Owner: "craig"}, "1")
	s.Save(SecureCredential{Name: "open", Type: SecureCredBearer, BaseURL: "https://b.example", Owner: "craig"}, "2")
	s.SetSecuredOwned("craig", "locked", true)

	sess := &ToolSession{Username: "craig"}
	if catalogHasTool(s, sess, "fetch_url_locked") {
		t.Error("the secured credential still has a tool")
	}
	if !catalogHasTool(s, sess, "fetch_url_open") {
		t.Error("securing one credential removed another user credential's tool")
	}
}

func catalogHasTool(s *SecureAPI, sess *ToolSession, name string) bool {
	for _, td := range s.BuildTools(sess) {
		if td.Tool.Name == name {
			return true
		}
	}
	return false
}
