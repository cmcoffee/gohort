package orchestrate

// Collections belong to the people who make them, so the routes that serve
// them are not part of the admin workbench.
//
// They were. apps/knowledge is where an ordinary person manages theirs, every
// call it makes lands on these routes, and the gate meant a non-admin opening
// Knowledge got "Agents is admin-only" and an empty page. The gate protected
// nothing: each handler already resolves by the session user, and what they
// may see and change is decided by LoadCollection and collectionWriteRefusal
// underneath.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTheCollectionRoutesAreNotAdminGated(t *testing.T) {
	raw, err := os.ReadFile("orchestrate.go")
	if err != nil {
		t.Fatalf("reading the routes: %v", err)
	}
	src := string(raw)
	// g is the admin gate. Any collections route wrapped in it is the bug.
	bad := regexp.MustCompile(`HandleFunc\("/api/collections[^"]*",\s*g\(`)
	if m := bad.FindAllString(src, -1); len(m) > 0 {
		t.Errorf("collections routes are behind the admin gate again: %v", m)
	}
	// And they are still registered at all.
	for _, route := range []string{`"/api/collections"`, `"/api/collections/"`, `"/api/collections/draft-description"`} {
		if !strings.Contains(src, "HandleFunc("+route) {
			t.Errorf("route %s is gone", route)
		}
	}
	// The agent routes came out from behind the gate too, later and for the
	// same reason: a user owns their agents as they own their collections.
	// What the gate is FOR now is the console — one administrator's view
	// across every user — which keeps it route by route in console.go.
	console, err := os.ReadFile("console.go")
	if err != nil {
		t.Fatalf("reading the console routes: %v", err)
	}
	if !strings.Contains(string(console), "g := T.adminGated") {
		t.Error("the console gave up its admin gate; that is the half the gate is for")
	}
}
