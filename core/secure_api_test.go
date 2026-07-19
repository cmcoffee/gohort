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
