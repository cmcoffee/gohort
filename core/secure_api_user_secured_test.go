package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

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
