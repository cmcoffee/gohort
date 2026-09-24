package servitor

// A shared command appliance shares the command, not the secrets it runs
// with: another user sees the EnvVars names, never the values.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func sharedCommandAppliance(t *testing.T) *Servitor {
	t.Helper()
	prev := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "bob", "pw", false)
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })

	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	UserDB(app.DB, "alice").Set(applianceTable, "cmd-1", Appliance{
		ID: "cmd-1", Name: "tool-a", Type: "command", Command: "tool-a", Owner: "alice", Shared: true,
		EnvVars: []string{"API_TOKEN=tok-0123456789", "MODE=1"},
	})
	app.setApplianceShared("cmd-1", "alice", true)
	return app
}

func TestSharedApplianceEnvValuesStayWithTheOwner(t *testing.T) {
	app := sharedCommandAppliance(t)

	for _, c := range []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
		path string
	}{
		{"one record", app.handleAppliance, "/api/appliance/cmd-1"},
		{"the list", app.handleAppliances, "/api/appliances"},
	} {
		w := httptest.NewRecorder()
		c.call(w, reqAs(t, http.MethodGet, c.path, "bob", ""))
		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Fatalf("%s: bob got %d", c.name, w.Code)
		}
		if strings.Contains(body, "tok-0123456789") {
			t.Errorf("%s: an EnvVars secret was returned to a user the appliance is only shared with", c.name)
		}
		if !strings.Contains(body, "API_TOKEN") {
			t.Errorf("%s: the variable names should still be visible: %s", c.name, body)
		}
	}

	w := httptest.NewRecorder()
	app.handleAppliance(w, reqAs(t, http.MethodGet, "/api/appliance/cmd-1", "alice", ""))
	if !strings.Contains(w.Body.String(), "tok-0123456789") {
		t.Error("the owner must still see their own values to edit them")
	}
}

func TestRedactedEnvVarsNeverBlankASecretOnSave(t *testing.T) {
	stored := []string{"API_TOKEN=tok-0123456789", "MODE=1"}
	got := restoreRedactedEnvVars(redactEnvVars(stored), stored)
	if strings.Join(got, ",") != strings.Join(stored, ",") {
		t.Errorf("round trip through the redacted form changed the record: %v", got)
	}
	if got := restoreRedactedEnvVars([]string{"API_TOKEN", "NEW=2"}, stored); strings.Join(got, ",") != "API_TOKEN=tok-0123456789,NEW=2" {
		t.Errorf("mixed update: %v", got)
	}
}

func TestSessionOutputHidesEnvValuesFromNonOwners(t *testing.T) {
	a := Appliance{Owner: "alice", EnvVars: []string{"API_TOKEN=tok-0123456789", "MODE=1"}}
	if !envHiddenFrom(a, "alice", "bob") || envHiddenFrom(a, "alice", "alice") {
		t.Fatal("hidden from the wrong user")
	}
	out := scrubEnvValues("API_TOKEN=tok-0123456789\nMODE=1\n", a.EnvVars)
	if strings.Contains(out, "tok-0123456789") {
		t.Errorf("value survived the scrub: %q", out)
	}
	if !strings.Contains(out, "MODE=1") {
		t.Errorf("a short non-secret value was mangled: %q", out)
	}
}
