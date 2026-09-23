package core

import (
	"bytes"
	"encoding/json"
	"github.com/cmcoffee/snugforge/kvlite"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

// Streaming file upload through the governed dispatch. The point is not that
// multipart works — stdlib does that — but that a file transfer is covered by
// the same allow-list, audit, and cancellation as every other outbound call,
// and that the bytes are never held twice.

// scopedFor mirrors what a LAN endpoint gets in production: an unauthenticated
// credential pinned to that one scheme+host. The legacy "no_auth" credential is
// https-only, so it cannot reach an httptest server — and shouldn't.
func scopedFor(url string) SecureCredential {
	return SecureCredential{
		Name:              "upload_test_local",
		Type:              SecureCredNone,
		AllowedURLPattern: imageHostPattern(url),
	}
}

func dispatchUploadScoped(t *testing.T, sess *ToolSession, url string, up FileUpload) (string, error) {
	t.Helper()
	return Secure().dispatch(scopedFor(url), map[string]any{
		"url": url, "method": "POST", secureUploadArg: &up,
	}, sess)
}

type uploadSeen struct {
	contentType string
	length      int64
	field       string
	filename    string
	body        []byte
	fields      map[string]string
}

func uploadEchoServer(t *testing.T, seen *uploadSeen, reply string) *httptest.Server {
	t.Helper()
	seen.fields = map[string]string{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.contentType = r.Header.Get("Content-Type")
		seen.length = r.ContentLength
		mt, params, err := mime.ParseMediaType(seen.contentType)
		if err != nil || !strings.HasPrefix(mt, "multipart/") {
			http.Error(w, "not multipart", http.StatusUnsupportedMediaType)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			raw, _ := io.ReadAll(part)
			if part.FileName() != "" {
				seen.field, seen.filename, seen.body = part.FormName(), part.FileName(), raw
			} else {
				seen.fields[part.FormName()] = string(raw)
			}
		}
		_, _ = w.Write([]byte(reply))
	}))
}

func TestDispatchUploadStreamsTheFile(t *testing.T) {
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{"ok":true}`)
	defer srv.Close()

	payload := bytes.Repeat([]byte("abcdefgh"), 4096) // 32 KB
	_, err := dispatchUploadScoped(t, &ToolSession{}, srv.URL+"/upload", FileUpload{
		Reader:    bytes.NewReader(payload),
		FieldName: "file",
		FileName:  "clip.mp3",
		Fields:    map[string]string{"model": "whisper-1", "response_format": "text"},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !bytes.Equal(seen.body, payload) {
		t.Errorf("received %d bytes, sent %d", len(seen.body), len(payload))
	}
	if seen.field != "file" || seen.filename != "clip.mp3" {
		t.Errorf("part = %q/%q", seen.field, seen.filename)
	}
	if seen.fields["model"] != "whisper-1" || seen.fields["response_format"] != "text" {
		t.Errorf("extra fields = %v", seen.fields)
	}
	// ContentLength -1 is the signal that mimebody wrapped the body in a
	// stream rather than buffering it to measure a length.
	if seen.length != -1 {
		t.Errorf("ContentLength = %d, want -1 (chunked/streamed)", seen.length)
	}
}

func TestUploadObeysTheCredentialAllowList(t *testing.T) {
	// The reason for routing uploads here at all. An http.Client of its own
	// would post user audio anywhere the endpoint config named.
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{}`)
	defer srv.Close()

	scoped := SecureCredential{
		Name:              "scoped_test",
		Type:              SecureCredNone,
		AllowedURLPattern: imageHostPattern(srv.URL),
	}
	_, err := Secure().dispatch(scoped, map[string]any{
		"url": "http://example.invalid/upload", "method": "POST",
		secureUploadArg: &FileUpload{Reader: bytes.NewReader([]byte("x")), FileName: "a.txt"},
	}, &ToolSession{})
	if err == nil {
		t.Fatal("an upload to a host outside the allow-list must be refused")
	}
	if len(seen.body) != 0 {
		t.Error("nothing should have reached the server")
	}
}

func TestUploadRefusedInPrivateMode(t *testing.T) {
	// Private mode is a turn-level kill switch on network egress. A file
	// transfer is exactly the call it most needs to stop.
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{}`)
	defer srv.Close()

	sess := &ToolSession{Network: NewNetworkConnector(true)}
	_, err := dispatchUploadScoped(t, sess, srv.URL+"/upload", FileUpload{
		Reader: bytes.NewReader([]byte("secret audio")), FileName: "a.mp3",
	})
	if err == nil {
		t.Fatal("Private mode must block an upload")
	}
	if !strings.Contains(err.Error(), "Private mode") {
		t.Errorf("error should name the cause: %v", err)
	}
	if len(seen.body) != 0 {
		t.Error("no bytes may leave the machine with egress off")
	}
}

func TestUploadFieldNameDefaults(t *testing.T) {
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{}`)
	defer srv.Close()

	if _, err := dispatchUploadScoped(t, &ToolSession{}, srv.URL+"/u", FileUpload{
		Reader: bytes.NewReader([]byte("hi")), FileName: "a.txt",
	}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if seen.field != "file" {
		t.Errorf("field = %q, want the \"file\" default", seen.field)
	}
}

func TestUploadTimeoutIsItsOwnTunable(t *testing.T) {
	// A file takes as long as the link allows; the 30s cap that suits an API
	// call would abort a photo on a slow uplink.
	up, ok := LookupTunable("tune_secure_api_upload_timeout")
	if !ok {
		t.Fatal("upload timeout is not a registered tunable")
	}
	req, _ := LookupTunable("tune_secure_api_request_timeout")
	if up.Default <= req.Default {
		t.Errorf("upload default %v must exceed the request default %v", up.Default, req.Default)
	}
	if up.Category != "Timeouts" {
		t.Errorf("category = %q", up.Category)
	}
}

// The dispatch ledger records WHO made the call, not only whose credential it
// was. Owner is a namespace; the person with their hands on it is a separate
// question, and one the ledger could not answer.
func TestTheLedgerRecordsWhoDispatched(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// A GLOBAL credential: anybody the grant admits dispatches through the same
	// ring, so "who" was never answerable from the rows.
	cred := SecureCredential{Name: "shared_api", Type: SecureCredNone,
		AllowedURLPattern: imageHostPattern(srv.URL)}
	if _, err := Secure().dispatch(cred, map[string]any{"url": srv.URL + "/v1/pages", "method": "GET"},
		&ToolSession{Username: "bob"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	got := Secure().LoadAudit("", "shared_api")
	if len(got) != 1 {
		t.Fatalf("expected one row, got %+v", got)
	}
	if got[0].DispatchedBy != "bob" {
		t.Errorf("the ledger does not say who called: %+v", got[0])
	}
	if got[0].Owner != "" {
		t.Errorf("a global credential's namespace is empty, got %q", got[0].Owner)
	}
}

// A call with no session behind it stays blank rather than being attributed to
// the credential's owner. Absence of a name is not evidence of one.
func TestAnUnattributedCallIsNotAttributedToTheOwner(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cred := SecureCredential{Name: "own_api", Type: SecureCredNone, Owner: "alice",
		AllowedURLPattern: imageHostPattern(srv.URL)}
	if _, err := Secure().dispatch(cred, map[string]any{"url": srv.URL + "/ping", "method": "GET"}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got := Secure().LoadAudit("alice", "own_api")
	if len(got) != 1 {
		t.Fatalf("expected one row, got %+v", got)
	}
	if got[0].DispatchedBy != "" {
		t.Errorf("a sessionless call was attributed to %q", got[0].DispatchedBy)
	}
}

// A failing call is recorded with its caller too: a refused write is exactly
// the row somebody asks "who tried that" about.
func TestAFailedCallStillNamesItsCaller(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	cred := SecureCredential{Name: "strict_api", Type: SecureCredNone,
		AllowedURLPattern: imageHostPattern(srv.URL)}
	_, _ = Secure().dispatch(cred, map[string]any{"url": srv.URL + "/v1/pages", "method": "POST", "body": "{}"},
		&ToolSession{Username: "bob"})

	got := Secure().LoadAudit("", "strict_api")
	if len(got) != 1 {
		t.Fatalf("expected one row, got %+v", got)
	}
	if got[0].Status != http.StatusForbidden || got[0].DispatchedBy != "bob" {
		t.Errorf("the refused row does not name its caller: %+v", got[0])
	}
}

// Rings written before the field existed still decode, and read as
// unattributed rather than failing or inventing an owner.
func TestLedgerRowsFromBeforeTheFieldDecode(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	s.db.Set(secureAPIAuditTable, "legacy", []SecureAPIAuditEntry{{CredentialName: "legacy", URL: "https://old", Status: 200}})
	got := s.LoadAudit("", "legacy")
	if len(got) != 1 || got[0].URL != "https://old" {
		t.Fatalf("pre-field ledger not readable: %+v", got)
	}
	if got[0].DispatchedBy != "" {
		t.Errorf("a pre-field row claims a caller: %q", got[0].DispatchedBy)
	}
}

// ----------------------------------------------------------------------
// Peer sharing of user-owned credentials
// ----------------------------------------------------------------------

func shareTestCred(t *testing.T, owner, name, pattern string) {
	t.Helper()
	if err := Secure().Save(SecureCredential{Name: name, Type: SecureCredNone, Owner: owner,
		AllowedURLPattern: pattern}, ""); err != nil {
		t.Fatalf("save %s/%s: %v", owner, name, err)
	}
}

// A credential a colleague lent you resolves, and appears as a tool you can
// actually call. A share that resolves to nothing is the decorative ACL every
// other primitive already learned not to ship.
func TestALentCredentialResolvesForItsRecipient(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	if err := Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil); err != nil {
		t.Fatalf("share: %v", err)
	}

	if _, ok := Secure().Resolve("wiki", "bob"); !ok {
		t.Fatal("the recipient cannot resolve the credential lent to them")
	}
	// Somebody unnamed gets nothing.
	if _, ok := Secure().Resolve("wiki", "dana"); ok {
		t.Error("an unnamed user resolved somebody else's credential")
	}
	if got := Secure().SharedWithUser("bob"); len(got) != 1 || got[0].Owner != "alice" {
		t.Errorf("the lend does not show up for the recipient: %+v", got)
	}
}

// The record is the source and the index is derived. Revoking on the record
// takes effect even though the setter also rewrites the index.
func TestRevokingALentCredentialTakesEffect(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	Secure().SetCredentialShares("alice", "wiki", nil, nil)
	if _, ok := Secure().Resolve("wiki", "bob"); ok {
		t.Error("a revoked share still resolves")
	}
	if got := Secure().SharedWithUser("bob"); len(got) != 0 {
		t.Errorf("a revoked share still lists: %+v", got)
	}
}

// Editing a credential's config must not silently revoke every share, the same
// way it does not silently re-enable or unsecure it.
func TestEditingACredentialKeepsItsShares(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, []string{"carol"})
	shareTestCred(t, "alice", "wiki", "https://wiki.example/v2/**")

	c, ok := Secure().LoadUser("alice", "wiki")
	if !ok {
		t.Fatal("load")
	}
	if len(c.SharedReadOnly) != 1 || len(c.SharedReadWrite) != 1 {
		t.Errorf("an edit dropped the shares: %+v / %+v", c.SharedReadOnly, c.SharedReadWrite)
	}
}

// The owner's own key always shadows one lent to them: a name they chose means
// the thing they made.
func TestYourOwnCredentialShadowsALentOne(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://alice.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	shareTestCred(t, "bob", "wiki", "https://bob.example/**")

	c, ok := Secure().Resolve("wiki", "bob")
	if !ok || c.Owner != "bob" {
		t.Errorf("the recipient's own credential did not win: %+v", c)
	}
}

// Two colleagues lending the same NAME is not a tie to break. Picking one would
// act as the wrong person, which is the whole risk the read/write split exists
// for, so nothing resolves.
func TestTwoLendersOfTheSameNameRefuseToResolve(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	shareTestCred(t, "carol", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	Secure().SetCredentialShares("carol", "wiki", []string{"bob"}, nil)

	if _, ok := Secure().Resolve("wiki", "bob"); ok {
		t.Error("an ambiguous lend resolved instead of refusing")
	}
}

// The point of two lists. A read-only lend reads and cannot write, whatever the
// owner's own use of the key allows.
func TestAReadOnlyLendCannotWrite(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	shareTestCred(t, "alice", "wiki", imageHostPattern(srv.URL))
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	c, _ := Secure().Resolve("wiki", "bob")

	if _, err := Secure().dispatch(c, map[string]any{"url": srv.URL + "/page", "method": "GET"},
		&ToolSession{Username: "bob"}); err != nil {
		t.Fatalf("a read through a read-only lend was refused: %v", err)
	}
	_, err := Secure().dispatch(c, map[string]any{"url": srv.URL + "/page", "method": "POST", "body": "{}"},
		&ToolSession{Username: "bob"})
	if err == nil {
		t.Fatal("a write through a read-only lend was allowed")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	// The OWNER is not narrowed by lending it out.
	if _, err := Secure().dispatch(c, map[string]any{"url": srv.URL + "/page", "method": "POST", "body": "{}"},
		&ToolSession{Username: "alice"}); err != nil {
		t.Errorf("the owner was narrowed by their own share: %v", err)
	}
}

// A read-write lend writes. The grant is a grant.
func TestAReadWriteLendCanWrite(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	shareTestCred(t, "alice", "wiki", imageHostPattern(srv.URL))
	Secure().SetCredentialShares("alice", "wiki", nil, []string{"bob"})
	c, _ := Secure().Resolve("wiki", "bob")

	if _, err := Secure().dispatch(c, map[string]any{"url": srv.URL + "/page", "method": "POST", "body": "{}"},
		&ToolSession{Username: "bob"}); err != nil {
		t.Errorf("a read-write lend could not write: %v", err)
	}
}

// A blocked attempt leaves a row. The refusal working is not a reason to say
// nothing: a colleague repeatedly trying to write through a read-only lend is
// exactly what its owner wants to see.
func TestABlockedWriteIsRecorded(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request reached the server")
	}))
	defer srv.Close()
	shareTestCred(t, "alice", "wiki", imageHostPattern(srv.URL))
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	c, _ := Secure().Resolve("wiki", "bob")
	_, _ = Secure().dispatch(c, map[string]any{"url": srv.URL + "/page", "method": "DELETE"},
		&ToolSession{Username: "bob"})

	got := Secure().LoadAudit("alice", "wiki")
	if len(got) != 1 {
		t.Fatalf("the blocked attempt left no row: %+v", got)
	}
	if got[0].DispatchedBy != "bob" {
		t.Errorf("the row does not name who tried: %+v", got[0])
	}
	// Status 0 means NOT SENT. A reader who cannot tell a refusal from a 403
	// learns the wrong thing from both.
	if got[0].Status != 0 || !strings.Contains(got[0].Error, "refused before sending") {
		t.Errorf("a refusal is not distinguishable from a rejection: %+v", got[0])
	}
}

// Read-write beats read-only for the same person, decided when it is stored
// rather than by whichever lookup happens to run first.
func TestOneNameInBothListsIsReadWrite(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob", "carol"}, []string{"bob"})

	c, _ := Secure().LoadUser("alice", "wiki")
	if credSliceHas(c.SharedReadOnly, "bob") {
		t.Errorf("bob is in both lists: %+v / %+v", c.SharedReadOnly, c.SharedReadWrite)
	}
	if !credSliceHas(c.SharedReadWrite, "bob") || !credSliceHas(c.SharedReadOnly, "carol") {
		t.Errorf("the grants did not survive normalization: %+v / %+v", c.SharedReadOnly, c.SharedReadWrite)
	}
	// Sharing with yourself is not a share.
	Secure().SetCredentialShares("alice", "wiki", []string{"alice"}, nil)
	c, _ = Secure().LoadUser("alice", "wiki")
	if len(c.SharedReadOnly) != 0 {
		t.Errorf("the owner ended up on their own share list: %+v", c.SharedReadOnly)
	}
}

// Deleting takes the lends with it, rather than leaving rows pointing at
// nothing.
func TestDeletingACredentialDropsItsLends(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	if err := Secure().DeleteUser("alice", "wiki"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := Secure().SharedWithUser("bob"); len(got) != 0 {
		t.Errorf("the recipient still holds a deleted credential: %+v", got)
	}
	if _, ok := Secure().Resolve("wiki", "bob"); ok {
		t.Error("a deleted credential still resolves for its recipient")
	}
}

// A disabled credential is nobody's to use, the owner's included. The admin's
// revoke lever has to reach the people it was lent to.
func TestDisablingACredentialReachesItsRecipients(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	if err := Secure().SetDisabledOwned("alice", "wiki", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok := Secure().Resolve("wiki", "bob"); ok {
		t.Error("a disabled credential still resolves for its recipient")
	}
}

// ----------------------------------------------------------------------
// Personal key -> service account
// ----------------------------------------------------------------------

// The move itself: the record and its secret leave the owner's namespace for
// the deployment's, and land SECURED. Secured is the whole mechanism — an open
// global credential would be usable by everybody through the auto-generated
// fetch_url tool, which is a far wider grant than "make this the team's key".
func TestHandingAKeyToTheDeploymentMovesItAndSecuresIt(t *testing.T) {
	secureAPITestStore(t)
	if err := Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**"}, "sk-the-key"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := promoteCredentialToService("alice", "wiki"); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if _, ok := Secure().LoadUser("alice", "wiki"); ok {
		t.Error("the credential is still in its former owner's namespace")
	}
	c, ok := Secure().Load("wiki")
	if !ok {
		t.Fatal("the deployment does not have it")
	}
	if c.Owner != "" {
		t.Errorf("it still claims an owner: %q", c.Owner)
	}
	if !c.Secured {
		t.Error("it landed unsecured, which hands the whole deployment a fetch_url tool for it")
	}
	// The secret came with it, and came with it ONCE.
	if got, ok := Secure().loadSecret("wiki"); !ok || got != "sk-the-key" {
		t.Errorf("the secret did not move: %q %v", got, ok)
	}
	if _, ok := Secure().loadSecret(credStoreKey("alice", "wiki")); ok {
		t.Error("a copy of the secret was left in the former owner's namespace")
	}
}

// The former owner is exactly that. They reach it the way everybody else does,
// which for a secured credential means through a bound tool.
func TestTheFormerOwnerHasNoSpecialClaim(t *testing.T) {
	secureAPITestStore(t)
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**"}, "sk-the-key")
	promoteCredentialToService("alice", "wiki")

	c, _ := Secure().Load("wiki")
	if !Secure().UserMayUse(c, "bob") || !Secure().UserMayUse(c, "alice") {
		t.Error("a deployment credential should pass the WHO axis for everyone; the lock is the tool bindings")
	}
	// And the tool catalog offers it to nobody directly, which is what secured
	// means: reachable through its bindings, not as fetch_url_wiki.
	for _, tool := range Secure().BuildTools(&ToolSession{Username: "alice"}) {
		if tool.Tool.Name == "fetch_url_wiki" {
			t.Error("a secured deployment credential is still a directly callable tool")
		}
	}
}

// Lends do not survive the handover. Everybody reaches it through the bindings
// now, so a list naming two people decides nothing, and leaving it would have a
// later un-securing silently restore an ACL from before the key changed hands.
func TestHandingOverClearsTheLends(t *testing.T) {
	secureAPITestStore(t)
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**"}, "sk-the-key")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, []string{"carol"})
	promoteCredentialToService("alice", "wiki")

	c, _ := Secure().Load("wiki")
	if len(c.SharedReadOnly) != 0 || len(c.SharedReadWrite) != 0 {
		t.Errorf("the lend lists survived: %+v / %+v", c.SharedReadOnly, c.SharedReadWrite)
	}
	if got := Secure().SharedWithUser("bob"); len(got) != 0 {
		t.Errorf("a borrower still holds it through the old index: %+v", got)
	}
}

// A name the deployment already uses is not a conflict to resolve by preferring
// one: every tool out there naming it would silently start dispatching through
// a different key, to a possibly different host, as a different identity.
func TestHandingOverRefusesToOverwriteADeploymentKey(t *testing.T) {
	secureAPITestStore(t)
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer,
		AllowedURLPattern: "https://wiki.example/**"}, "the-deployments")
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice",
		AllowedURLPattern: "https://other.example/**"}, "alices")

	err := promoteCredentialToService("alice", "wiki")
	if err == nil {
		t.Fatal("the handover overwrote a deployment credential")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("the refusal does not say what to do about it: %v", err)
	}
	if got, _ := Secure().loadSecret("wiki"); got != "the-deployments" {
		t.Errorf("the deployment's secret was replaced: %q", got)
	}
	if _, ok := Secure().LoadUser("alice", "wiki"); !ok {
		t.Error("a refused handover consumed the owner's credential anyway")
	}
}

// The tools that already dispatch through it become its bindings, because those
// are the tools it was built for. A "secret" tool — one handed the raw key for a
// script — is left out: securing blocks that path, so binding it would record an
// approval for something that cannot work.
func TestTheDeclaringToolsBecomeTheBindings(t *testing.T) {
	secureAPITestStore(t)
	saved := CredentialToolsResolver
	CredentialToolsResolver = func(cred string) []CredentialToolRef {
		return []CredentialToolRef{
			{Tool: "wiki_search", Via: "fetch_via"},
			{Tool: "wiki_page", Via: "api"},
			{Tool: "wiki_search", Via: "fetch_via"}, // the same tool on a second agent
			{Tool: "wiki_dump", Via: "secret"},
		}
	}
	t.Cleanup(func() { CredentialToolsResolver = saved })

	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**"}, "sk-the-key")
	if err := promoteCredentialToService("alice", "wiki"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	c, _ := Secure().Load("wiki")
	want := []string{"wiki_page", "wiki_search"}
	if len(c.ApprovedToolBindings) != len(want) {
		t.Fatalf("bindings = %v, want %v", c.ApprovedToolBindings, want)
	}
	for i, w := range want {
		if c.ApprovedToolBindings[i] != w {
			t.Errorf("bindings = %v, want %v", c.ApprovedToolBindings, want)
			break
		}
	}
}

// Nothing to hand over is an error, not a silent no-op that reports success.
func TestHandingOverSomethingYouDoNotOwnFails(t *testing.T) {
	secureAPITestStore(t)
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**"}, "sk-the-key")
	if err := promoteCredentialToService("bob", "wiki"); err == nil {
		t.Error("somebody handed over a credential that was not theirs")
	}
	if _, ok := Secure().LoadUser("alice", "wiki"); !ok {
		t.Error("the real owner's credential was disturbed")
	}
}

// ----------------------------------------------------------------------
// Lending policy
// ----------------------------------------------------------------------

// Unset behaves exactly as before. A policy that defaulted to refusing would
// have revoked every lend already made the moment the field existed.
func TestAnUndecidedKeyLendsAsItAlwaysDid(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	if err := Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, []string{"carol"}); err != nil {
		t.Fatalf("an undecided key refused a lend: %v", err)
	}
	c, _ := Secure().LoadUser("alice", "wiki")
	if lend, write := c.MayLend(); !lend || !write {
		t.Errorf("unset reads as lend=%v write=%v", lend, write)
	}
}

// The refusal is on the SETTER, not only in the flow that offers the options.
// A flow that merely declined to offer would be one refusal any other door
// walks straight past.
func TestAKeySetToNeverLendRefusesEveryDoor(t *testing.T) {
	secureAPITestStore(t)
	if err := Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredNone, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**", Lending: LendNone}, ""); err != nil {
		t.Fatalf("save: %v", err)
	}
	err := Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	if err == nil {
		t.Fatal("a key set to never lend was lent anyway")
	}
	// The refusal says what to change, or the reader tries the same thing
	// somewhere else.
	if !strings.Contains(err.Error(), "credential itself") {
		t.Errorf("the refusal does not say where to change it: %v", err)
	}
	// And taking a lend BACK is never refused: clearing is not lending.
	if err := Secure().SetCredentialShares("alice", "wiki", nil, nil); err != nil {
		t.Errorf("revoking was refused by the lending policy: %v", err)
	}
}

// Reads-only is a narrowing, not a ban: the read lend goes through and the
// write lend does not.
func TestAReadsOnlyKeyRefusesOnlyTheWriteLend(t *testing.T) {
	secureAPITestStore(t)
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredNone, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**", Lending: LendRead}, "")

	if err := Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil); err != nil {
		t.Errorf("a read lend was refused: %v", err)
	}
	if err := Secure().SetCredentialShares("alice", "wiki", nil, []string{"bob"}); err == nil {
		t.Error("a write lend went through on a reads-only key")
	}
}

// Tightening reaches what already happened. A policy saying nobody, over a key
// two people hold, would be a rule about the future pretending to be a rule.
func TestTighteningThePolicyTakesBackWhatItForbids(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, []string{"carol"})

	// Narrowed to reads: carol keeps the key and loses the writing.
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredNone, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**", Lending: LendRead}, "")
	c, _ := Secure().LoadUser("alice", "wiki")
	if len(c.SharedReadWrite) != 0 {
		t.Errorf("a write lend survived a narrowing: %+v", c.SharedReadWrite)
	}
	if !credSliceHas(c.SharedReadOnly, "carol") || !credSliceHas(c.SharedReadOnly, "bob") {
		t.Errorf("narrowing took the key away rather than the writing: %+v", c.SharedReadOnly)
	}

	// Set to nobody: both lose it outright.
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredNone, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**", Lending: LendNone}, "")
	c, _ = Secure().LoadUser("alice", "wiki")
	if len(c.SharedReadOnly)+len(c.SharedReadWrite) != 0 {
		t.Errorf("a lend survived the policy: %+v / %+v", c.SharedReadOnly, c.SharedReadWrite)
	}
	if got := Secure().SharedWithUser("bob"); len(got) != 0 {
		t.Errorf("the recipient still resolves it: %+v", got)
	}
}

// ----------------------------------------------------------------------
// Agent-scoped lends
// ----------------------------------------------------------------------

// The point: a key lent so somebody could run one agent works inside that
// agent and nowhere else. Without this, lending for one agent handed them a
// key they could spend from any agent of their own, or from a tool they wrote
// that afternoon — a grant far wider than the reason for it.
func TestALendForOneAgentWorksOnlyThere(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	if err := Secure().LendForAgent("alice", "wiki", "agent-1", []string{"bob"}, false); err != nil {
		t.Fatalf("lend: %v", err)
	}

	if _, ok := Secure().ResolveIn("wiki", "bob", "agent-1"); !ok {
		t.Error("the lend does not work in the agent it was made for")
	}
	if _, ok := Secure().ResolveIn("wiki", "bob", "agent-2"); ok {
		t.Error("the lend works from another of their agents")
	}
	// Fails closed: a caller that cannot say which agent it is running is
	// outside the scope, not inside all of them.
	if _, ok := Secure().ResolveIn("wiki", "bob", ""); ok {
		t.Error("the lend resolved with no agent in context")
	}
	// And it does not appear in their general catalog, which would offer a
	// capability that refuses when called.
	if got := Secure().SharedWithUserIn("bob", "agent-2"); len(got) != 0 {
		t.Errorf("a scoped lend is listed outside its agent: %+v", got)
	}
	if got := Secure().SharedWithUserIn("bob", "agent-1"); len(got) != 1 {
		t.Errorf("a scoped lend is missing inside its agent: %+v", got)
	}
}

// Lends made before the field existed, and lends made from the credential card
// on purpose, stay unscoped. A scope that defaulted to "one agent" would have
// broken every existing lend on the first read.
func TestAnUnscopedLendStillWorksEverywhere(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)

	for _, agent := range []string{"agent-1", "agent-2", ""} {
		if _, ok := Secure().ResolveIn("wiki", "bob", agent); !ok {
			t.Errorf("an unscoped lend failed for agent %q", agent)
		}
	}
}

// Narrowing a grant somebody already holds is a different act from making one.
// Doing it as a side effect of sharing an agent would quietly take away access
// that was granted on purpose.
func TestLendingForAnAgentDoesNotNarrowAnExistingGrant(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	if err := Secure().LendForAgent("alice", "wiki", "agent-1", []string{"bob"}, false); err != nil {
		t.Fatalf("lend: %v", err)
	}
	if _, ok := Secure().ResolveIn("wiki", "bob", "agent-2"); !ok {
		t.Error("an existing unscoped lend was narrowed by an agent share")
	}
}

// The scope follows the lend. An entry for somebody who no longer holds the
// key would outlive the grant it narrowed, and be waiting to narrow the next
// one made for another reason entirely.
func TestRevokingALendDropsItsScope(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().LendForAgent("alice", "wiki", "agent-1", []string{"bob"}, false)
	Secure().SetCredentialShares("alice", "wiki", nil, nil)

	c, _ := Secure().LoadUser("alice", "wiki")
	if len(c.SharedForAgents) != 0 {
		t.Errorf("the scope outlived the lend: %+v", c.SharedForAgents)
	}
	// Re-lending unscoped is now genuinely unscoped rather than inheriting a
	// narrowing nobody asked for.
	Secure().SetCredentialShares("alice", "wiki", []string{"bob"}, nil)
	if _, ok := Secure().ResolveIn("wiki", "bob", "agent-9"); !ok {
		t.Error("a fresh unscoped lend inherited a stale scope")
	}
}

// A write lend scopes the same way, and stays a write lend.
func TestAScopedWriteLendIsStillAWriteLend(t *testing.T) {
	secureAPITestStore(t)
	shareTestCred(t, "alice", "wiki", "https://wiki.example/**")
	Secure().LendForAgent("alice", "wiki", "agent-1", []string{"bob"}, true)

	c, _ := Secure().LoadUser("alice", "wiki")
	if !credSliceHas(c.SharedReadWrite, "bob") {
		t.Errorf("the write lend did not land: %+v", c.SharedReadWrite)
	}
	if credShareGrantIn(c, "bob", "agent-1") != credShareWrite {
		t.Error("it is not a write grant inside its agent")
	}
	if credShareGrantIn(c, "bob", "agent-2") != credShareNone {
		t.Error("it is a grant outside its agent")
	}
}

// An edit must not zero what the form does not carry.
//
// Save takes a whole SecureCredential, and the upsert forms build a PARTIAL
// one - nine fields from Extensions, a few more from admin. Every field
// outside that set was overwritten with its zero value by any edit, however
// unrelated: change a base URL, lose the rest.
//
// The damage is invisible at the moment it happens and shows up later as
// something else entirely. CredScope decides which key the secret is read
// from, so clearing it on a per_user credential moves the lookup and the
// credential reports "no stored secret" while the secret sits untouched under
// the old key. That is a key that "just vanished".
func TestAnEditDoesNotZeroWhatTheFormDoesNotCarry(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}

	full := SecureCredential{
		Name: "gitlab", Type: SecureCredBearer, BaseURL: "https://git.example",
		Owner: "alice", CredScope: "per_user",
		SharedReadOnly: []string{"bob"}, SharedReadWrite: []string{"carol"},
		SharedForAgents:      map[string][]string{"bob": {"agent-1"}},
		ApprovedToolBindings: []string{"gitlab_list_projects"},
		RevokedToolBindings:  []string{"gitlab_delete"},
		Secured:              true,
	}
	if err := s.Save(full, "glpat-original"); err != nil {
		t.Fatal(err)
	}

	// The shape an upsert form posts: the handful of fields it owns, and
	// nothing else. Blank secret = keep the stored one.
	if err := s.Save(SecureCredential{
		Name: "gitlab", Type: SecureCredBearer, BaseURL: "https://git.example/v2",
		Owner: "alice",
	}, ""); err != nil {
		t.Fatal(err)
	}

	got, ok := s.LoadUser("alice", "gitlab")
	if !ok {
		t.Fatal("the credential is gone entirely")
	}
	// What the form DID carry changed.
	if got.BaseURL != "https://git.example/v2" {
		t.Errorf("the edit did not apply: %q", got.BaseURL)
	}
	// What it did not carry survived.
	if got.CredScope != "per_user" {
		t.Errorf("CredScope was zeroed: %q - the secret lookup has moved and the key reads as missing", got.CredScope)
	}
	if len(got.SharedForAgents["bob"]) != 1 {
		t.Errorf("SharedForAgents was zeroed: %v - a lend scoped to one agent silently widened", got.SharedForAgents)
	}
	if len(got.ApprovedToolBindings) != 1 || len(got.RevokedToolBindings) != 1 {
		t.Errorf("the tool bindings were zeroed: approved=%v revoked=%v", got.ApprovedToolBindings, got.RevokedToolBindings)
	}
	// And the ones that were already preserved still are.
	if !got.Secured || len(got.SharedReadOnly) != 1 || len(got.SharedReadWrite) != 1 {
		t.Errorf("a previously-preserved field regressed: secured=%v ro=%v rw=%v",
			got.Secured, got.SharedReadOnly, got.SharedReadWrite)
	}
	// The secret is untouched by any of it.
	if v, _, found := s.readSecretAt(credStoreKey("alice", "gitlab")); !found || v != "glpat-original" {
		t.Errorf("the stored secret changed on an edit: found=%v", found)
	}
}

// Every credential ships its fetch_url_<name> schema on every turn, so the
// generic part of it — everything that is NOT this credential's name, allowed
// URLs or description — is paid once per credential. It was ~1,100 bytes of
// identical prose five times over on a five-credential deployment. Guard the
// slim version, and that each parameter still explains itself: an allowlist can
// grant fetch_url_<name> without fetch_url, so none may defer to it.
func TestCredentialToolSchemaStaysSlim(t *testing.T) {
	s := &SecureAPI{}
	c := SecureCredential{Name: "x", AllowedURLPattern: "", Description: ""}
	td := s.agentToolFromCredential(c, nil)
	b, err := json.Marshal(td.Tool)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 800 {
		t.Errorf("generic credential-tool schema is %d bytes; keep the shared prose under 800 (it is paid once per credential)", len(b))
	}
	for _, p := range []string{"url", "method", "body", "request_headers", "save_to"} {
		d := td.Tool.Parameters[p].Description
		if strings.TrimSpace(d) == "" {
			t.Errorf("%s lost its description", p)
		}
		if strings.Contains(d, "fetch_url") {
			t.Errorf("%s defers to fetch_url, which the agent may not have: %q", p, d)
		}
	}
}

// An empty allow-list means every path under the base URL, and must say so
// rather than rendering as "Allowed URLs: ." (seen live on a credential with no
// pattern, where it read as a one-character allow-list).
func TestCredentialToolEmptyPatternReadsAsOpen(t *testing.T) {
	d := (&SecureAPI{}).agentToolFromCredential(SecureCredential{Name: "x"}, nil).Tool.Description
	if strings.Contains(d, "Allowed URLs: .") || !strings.Contains(d, "any path under the API's base URL") {
		t.Fatalf("empty pattern must read as open, got %q", d)
	}
}
