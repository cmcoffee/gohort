package admin

// Admin › Activity lists what was done and by whom, and only what was done:
// a refused request is not a change. A credential is logged by its name,
// never its secret.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestTheActivityPageListsWhatAdministratorsDid(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(db, "root", "pw", true)
	AuthSetUser(db, "bob", "pw", false)
	adminToken, bobToken := AuthCreateSession(db, "root"), AuthCreateSession(db, "bob")
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerUsersRoutes(mux)
	a.registerCredentialsRoutes(mux)
	a.registerActivityRoutes(mux)
	do := func(token, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	if w := do(adminToken, "POST", "/api/users", `{"username":"carol","password":"pw-c-123456","admin":true}`); w.Code >= 400 {
		t.Fatalf("add user: %d %s", w.Code, w.Body.String())
	}
	if w := do(adminToken, "DELETE", "/api/users/nobody", ""); w.Code < 400 {
		t.Fatalf("precondition: deleting a missing account should be refused, got %d", w.Code)
	}
	if w := do(adminToken, "DELETE", "/api/users/carol", ""); w.Code >= 400 {
		t.Fatalf("delete user: %d %s", w.Code, w.Body.String())
	}
	if w := do(adminToken, "POST", "/api/secure-api", `{"name":"zz_act_cred","type":"bearer","base_url":"https://api.example.test","secret":"s3cr3t-value"}`); w.Code != 204 {
		t.Fatalf("save credential: %d %s", w.Code, w.Body.String())
	}

	w := do(adminToken, "GET", "/api/activity", "")
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	details := []string{}
	for _, r := range rows {
		details = append(details, r["kind"].(string)+": "+r["detail"].(string))
		if r["user"] != "root" {
			t.Errorf("the change should name who made it: %+v", r)
		}
	}
	got := strings.Join(details, " | ")
	for _, want := range []string{
		"account: added the account carol as an administrator",
		"account: deleted the account carol",
		"credential: saved the credential zz_act_cred",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "nobody") {
		t.Error("a refused delete was logged as done")
	}
	if strings.Contains(w.Body.String(), "s3cr3t-value") {
		t.Fatal("a secret reached the activity log")
	}
	if rows[0]["detail"] != "saved the credential zz_act_cred" {
		t.Errorf("newest first: %+v", rows[0])
	}

	if w := do(adminToken, "GET", "/api/activity?kind="+activity.Credential, ""); strings.Count(w.Body.String(), `"kind"`) != 1 {
		t.Errorf("kind filter: %s", w.Body.String())
	}
	if w := do(bobToken, "GET", "/api/activity", ""); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin read the log: %d", w.Code)
	}
}

// The page draws the log on its own tab.
func TestTheActivityLogHasItsOwnTab(t *testing.T) {
	secs := (&AdminApp{}).activitySections()
	if len(secs) != 1 || secs[0].Title != "Activity Log" || sectionTab(secs[0].Title, "") != "Activity" {
		t.Fatalf("the activity log should be its own tab: %+v", secs)
	}
	b, _ := json.Marshal(secs[0])
	if !strings.Contains(string(b), `"source":"api/activity"`) {
		t.Error("the section does not read the log")
	}
}
