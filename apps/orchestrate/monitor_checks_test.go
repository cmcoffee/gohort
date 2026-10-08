package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A monitor's recent checks read newest first, the latest rides the one-line
// listings, and the Scheduler's Recent checks button answers only for the
// caller's own monitor.
func TestRecentChecksReadNewestFirstAndOnlyForTheOwner(t *testing.T) {
	var m EventMonitor
	raw := `{"name":"cve-watch","owner":"u","kind":"poll","recent_checks":[
		{"at":"2026-10-07T10:00:00Z","outcome":"no match","detail":"answer lacks \"YES\": NONE"},
		{"at":"2026-10-07T11:00:00Z","outcome":"held","detail":"still matches since it fired"}]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	lines := monitorCheckLines(m, 5)
	if len(lines) != 2 || !strings.Contains(lines[0], "held: still matches") || !strings.Contains(lines[1], "no match") {
		t.Fatalf("lines = %q", lines)
	}
	if n := lastCheckNote(m); !strings.HasPrefix(n, "; last check ") || !strings.Contains(n, "held") {
		t.Fatalf("listing note = %q", n)
	}
	if lastCheckNote(EventMonitor{}) != "" {
		t.Error("a monitor with no checks adds nothing to its listing")
	}

	depStores(t, "u", "other")
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	SaveEventMonitor(RootDB, m)
	app := &OrchestrateApp{AppCore: AppCore{DB: orchestrateBaseDB}}
	ask := func(who string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/console/monitors/checks?id=cve-watch", nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), who)})
		w := httptest.NewRecorder()
		app.handleConsoleMonitorChecks(w, r)
		return w
	}
	if w := ask("u"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "held: still matches") {
		t.Fatalf("owner got %d %s", w.Code, w.Body.String())
	}
	if w := ask("other"); w.Code != http.StatusNotFound {
		t.Fatalf("another user got %d %s", w.Code, w.Body.String())
	}
}
