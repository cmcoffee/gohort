package servitor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The edit form's list is checked when saved: an entry the gate cannot read is
// refused with the reason, and a good list is stored the way the gate reads it.
func TestSavingLookupDestinations(t *testing.T) {
	app := sharedCommandAppliance(t)
	save := func(list string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		body := `{"type":"ssh","name":"web01","host":"web01.corp.example.com","lookup_allow":` + list + `}`
		routed(app)(w, reqAs(t, http.MethodPost, "/api/appliances", "alice", body))
		return w
	}
	if w := save(`["com"]`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "top-level domain") {
		t.Fatalf("a top-level domain was not refused: %d %s", w.Code, w.Body.String())
	}
	if w := save(`["*.Example.NET", "10.20.0.7/16"]`); w.Code >= 300 {
		t.Fatalf("a good list was refused: %d %s", w.Code, w.Body.String())
	}
	var a Appliance
	udb := UserDB(app.DB, "alice")
	for _, k := range udb.Keys(applianceTable) {
		if k != "cmd-1" {
			udb.Get(applianceTable, k, &a)
		}
	}
	if strings.Join(a.LookupAllow, ",") != "example.net,10.20.0.0/16" {
		t.Errorf("stored %q", a.LookupAllow)
	}
	if hits := assess_command("dig www.example.net", "", lookup_scope_for(a)); len(hits) != 0 {
		t.Errorf("a saved destination still asks: %+v", hits)
	}
}
