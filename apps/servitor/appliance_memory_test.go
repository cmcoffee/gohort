package servitor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestApplianceMemScope locks the synthetic scope-user format the orchestrate
// seeding APIs + the ...ForScope handlers key off. Drifting this silently
// detaches the modal from whatever the lead writes.
func TestApplianceMemScope(t *testing.T) {
	if got := applianceMemScope("abc-123"); got != "app:servitor:abc-123" {
		t.Fatalf("scope = %q, want app:servitor:abc-123", got)
	}
}

// TestApplianceMemoryModalWiring guards the two seams that must agree across
// the chat-page toolbar button, the shared modal builder, and the JS base
// resolver. If any drifts, the Memory button opens a modal that fetches the
// wrong place (or nothing).
func TestApplianceMemoryModalWiring(t *testing.T) {
	s := applianceMemoryModalScript
	// The toolbar action in chat_page.go targets this exact name.
	if !strings.Contains(s, "'servitor_appliance_memory'") {
		t.Error("modal script missing the servitor_appliance_memory client action")
	}
	// The base is resolved at open via servitorMemBase() (web_assets.go).
	if !strings.Contains(s, "var MEMBASE = (servitorMemBase());") {
		t.Error("modal script missing the servitorMemBase() base resolver")
	}
	// Endpoints must build off MEMBASE, not a baked literal — spot-check the
	// ones handleApplianceMemory routes.
	for _, ep := range []string{"MEMBASE + 'facts'", "MEMBASE + 'graph'", "MEMBASE + 'inferred'", "MEMBASE + 'agent'"} {
		if !strings.Contains(s, ep) {
			t.Errorf("modal script missing endpoint %q", ep)
		}
	}
}

// Someone a system is shared with can read what it remembers, but only its
// owner can change it: the investigator acts on those notes with the owner's
// own login.
func TestOnlyASystemsOwnerChangesWhatItRemembers(t *testing.T) {
	prevAuth := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prevAuth })
	auth.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	auth.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})

	app, udb := sourcesApp(t)
	a := Appliance{ID: "lab-box", Name: "Lab Box", Type: "ssh", Owner: "alice", AllowedUsers: []string{"bob"}}
	udb.Set(applianceTable, a.ID, a)
	app.indexNamedShares("alice", a.ID, a.AllowedUsers)

	send := func(user, method string) int {
		r := httptest.NewRequest(method, "/api/appliances/lab-box/notes", strings.NewReader(`{"note":"x"}`))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		w := httptest.NewRecorder()
		app.handleApplianceMemory(w, r)
		return w.Code
	}
	if code := send("bob", "POST"); code != http.StatusForbidden {
		t.Errorf("a recipient's write = %d, want 403", code)
	}
	if code := send("alice", "POST"); code == http.StatusForbidden || code == http.StatusNotFound {
		t.Errorf("the owner's write = %d", code)
	}
	if code := send("bob", "GET"); code == http.StatusForbidden || code == http.StatusNotFound {
		t.Errorf("a recipient's read = %d", code)
	}
}
