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

// TestSecuredToolBindingLifecycle covers P1 of the secured-credential tool
// binding: request → approve → (dispatch-authoritative) → revoke, with
// idempotence. This allowlist is how a secured cred's access follows TOOL scope.
func TestSecuredToolBindingLifecycle(t *testing.T) {
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	s.db.Set(secureAPITable, "ts3_api", SecureCredential{Name: "ts3_api", Secured: true})

	if s.ToolBindingApproved("ts3_api", "ts3_status") {
		t.Fatal("no binding approved initially")
	}

	// Request → pending, not yet approved.
	if err := s.RequestToolBinding("ts3_api", "ts3_status"); err != nil {
		t.Fatalf("request: %v", err)
	}
	c, _ := s.Load("ts3_api")
	if !credSliceHas(c.PendingToolBindings, "ts3_status") {
		t.Fatalf("expected pending binding, got %v", c.PendingToolBindings)
	}
	if s.ToolBindingApproved("ts3_api", "ts3_status") {
		t.Fatal("pending is not approved")
	}
	// Idempotent request.
	_ = s.RequestToolBinding("ts3_api", "ts3_status")
	c, _ = s.Load("ts3_api")
	if len(c.PendingToolBindings) != 1 {
		t.Fatalf("request must be idempotent; pending=%v", c.PendingToolBindings)
	}

	// Approve → approved, pending cleared.
	if err := s.ApproveToolBinding("ts3_api", "ts3_status"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !s.ToolBindingApproved("ts3_api", "ts3_status") {
		t.Fatal("should be approved after ApproveToolBinding")
	}
	c, _ = s.Load("ts3_api")
	if credSliceHas(c.PendingToolBindings, "ts3_status") {
		t.Fatal("pending must clear on approve")
	}

	// Revoke → gone from both.
	if err := s.RevokeToolBinding("ts3_api", "ts3_status"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s.ToolBindingApproved("ts3_api", "ts3_status") {
		t.Fatal("should be revoked")
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
