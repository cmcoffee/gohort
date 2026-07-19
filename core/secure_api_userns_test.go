package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

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
	mk("shared", "", "gk")     // global
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
