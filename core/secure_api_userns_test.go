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
	mk("shared", "")    // global — must NOT appear in ListAllUserOwned
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
