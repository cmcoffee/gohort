package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

func withCheck(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: appscript.CheckScratchCookie, Value: token})
	return r
}

// A page check's requests read and write its scratch records alone: the
// owner's records are not seen and not changed, a check that is over (or is
// for another app) is refused rather than read as no check, and the routes
// that act on their own are closed to it.
func TestAPageChecksRequestsTouchOnlyItsScratchRecords(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "tally", Name: "Tally", Owner: "u", RecordKey: "id",
		Sections: json.RawMessage(`[{"kind":"table","columns":[{"field":"item"}]}]`)}
	if w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/tally/records", strings.NewReader(`{"id":"real","item":"the owner's"}`))); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	token, end := appscript.BeginCheckScratch("u", "tally", "id", []map[string]any{{"id": "seeded", "item": "from the check"}})

	w := appAs(t, T, spec, "u", withCheck(httptest.NewRequest("GET", "/apps/tally/records", nil), token))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "from the check") || strings.Contains(w.Body.String(), "the owner's") {
		t.Fatalf("a check's read is not its scratch alone: %d %s", w.Code, w.Body.String())
	}
	if w := appAs(t, T, spec, "u", withCheck(httptest.NewRequest("POST", "/apps/tally/records", strings.NewReader(`{"id":"added","item":"by the check"}`)), token)); w.Code != http.StatusOK {
		t.Fatalf("a check's write: %d %s", w.Code, w.Body.String())
	}
	owners := appscript.ReadRecords(T.recordBase(spec, "u"), "tally")
	if len(owners) != 1 || owners[0]["item"] != "the owner's" {
		t.Errorf("a check's write reached the owner's records: %+v", owners)
	}
	for _, path := range []string{"/apps/tally/chat", "/apps/tally/pipeline", "/apps/tally/file"} {
		if w := appAs(t, T, spec, "u", withCheck(httptest.NewRequest("POST", path, strings.NewReader(`{}`)), token)); w.Code != http.StatusForbidden {
			t.Errorf("%s was open to a check: %d", path, w.Code)
		}
	}
	if w := appAs(t, T, spec, "u", withCheck(httptest.NewRequest("POST", "/apps/tally/_settings/values", strings.NewReader(`{}`)), token)); w.Code != http.StatusForbidden {
		t.Errorf("a check changed the app's settings: %d", w.Code)
	}

	end()
	if w := appAs(t, T, spec, "u", withCheck(httptest.NewRequest("GET", "/apps/tally/records", nil), token)); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "the owner's") {
		t.Errorf("a check that is over was read as no check: %d %s", w.Code, w.Body.String())
	}
	other, endOther := appscript.BeginCheckScratch("u", "another", "id", nil)
	defer endOther()
	if w := appAs(t, T, spec, "u", withCheck(httptest.NewRequest("GET", "/apps/tally/records", nil), other)); w.Code != http.StatusForbidden {
		t.Errorf("another app's check reached this one: %d", w.Code)
	}
}
