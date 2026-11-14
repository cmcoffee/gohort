package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/oidc"
	"github.com/cmcoffee/snugforge/kvlite"
)

const (
	ssoTenant = "11111111-2222-3333-4444-555555555555"
	ssoClient = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

type ssoAdmin struct {
	db         *DBase
	admin, bob string
	mux        *http.ServeMux
}

func newSSOAdmin(t *testing.T) *ssoAdmin {
	t.Helper()
	f := &ssoAdmin{db: &DBase{Store: kvlite.MemStore()}}
	prevRoot, prevAuth, prevBase := RootDB, AuthDB, WebBaseURL
	RootDB = f.db
	AuthDB = func() Database { return f.db }
	WebBaseURL = func() string { return "https://gohort.example.test/" }
	t.Cleanup(func() { RootDB, AuthDB, WebBaseURL = prevRoot, prevAuth, prevBase })
	AuthSetUser(f.db, "root", "pw", true)
	AuthSetUser(f.db, "bob", "pw", false)
	f.admin, f.bob = AuthCreateSession(f.db, "root"), AuthCreateSession(f.db, "bob")
	a := &AdminApp{db: f.db}
	f.mux = http.NewServeMux()
	a.registerSSORoutes(f.mux)
	return f
}

func (f *ssoAdmin) do(token, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *ssoAdmin) get(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	w := f.do(f.admin, "GET", "/api/sso", "")
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return out
}

// Set up as the section's steps say: the IDs, a certificate, then on.
func TestMicrosoftSignInTurnsOnOnlyOnceItCanWork(t *testing.T) {
	f := newSSOAdmin(t)
	st := f.get(t)
	if st["redirect_uri"] != "https://gohort.example.test/login/sso/callback" || st["enabled"] != false || st["new_people"] != oidc.NewPeopleApproval {
		t.Fatalf("defaults %+v", st)
	}
	for _, body := range []string{`{"tenant":"common"}`, `{"client_id":"my-app"}`, `{"credential":"password"}`, `{"new_people":"everyone"}`} {
		if w := f.do(f.admin, "PATCH", "/api/sso", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s saved: %d", body, w.Code)
		}
	}
	if w := f.do(f.admin, "PATCH", "/api/sso", `{"tenant":"`+ssoTenant+`","client_id":"`+ssoClient+`"}`); w.Code != 200 {
		t.Fatalf("ids: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.admin, "PATCH", "/api/sso", `{"enabled":true}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "certificate") {
		t.Fatalf("turned on with no certificate: %d %s", w.Code, w.Body.String())
	}
	if st := f.get(t); !strings.Contains(st["status"].(string), "make one") {
		t.Errorf("status %q", st["status"])
	}
	if w := f.do(f.admin, "POST", "/api/sso/certificate", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "thumbprint") {
		t.Fatalf("make certificate: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.admin, "PATCH", "/api/sso", `{"enabled":true}`); w.Code != 200 {
		t.Fatalf("turn on: %d %s", w.Code, w.Body.String())
	}
	st = f.get(t)
	if st["enabled"] != true || !strings.HasPrefix(st["status"].(string), "On: the login page offers") || st["cert_download"] == "" {
		t.Errorf("after turning on: %+v", st)
	}
	dl := f.do(f.admin, "GET", "/api/sso/certificate.cer", "")
	if dl.Code != 200 || !strings.Contains(dl.Body.String(), "BEGIN CERTIFICATE") || strings.Contains(dl.Body.String(), "PRIVATE KEY") {
		t.Errorf("download %d: %s", dl.Code, dl.Body.String())
	}
	got := []string{}
	for _, e := range activity.List(f.db, activity.Filter{}) {
		got = append(got, e.Detail)
	}
	all := strings.Join(got, " | ")
	for _, want := range []string{"turned Microsoft sign-in on", "made a Microsoft sign-in certificate", "set Microsoft sign-in tenant"} {
		if !strings.Contains(all, want) {
			t.Errorf("activity lacks %q: %s", want, all)
		}
	}
}

func TestAClientSecretIsKeptAndNeverShown(t *testing.T) {
	f := newSSOAdmin(t)
	body := `{"tenant":"` + ssoTenant + `","client_id":"` + ssoClient + `","credential":"secret","client_secret":"s3cr3t-value","enabled":true}`
	if w := f.do(f.admin, "PATCH", "/api/sso", body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if st := f.get(t); st["client_secret"] != secretPlaceholder || st["enabled"] != true {
		t.Fatalf("after save %+v", st)
	}
	// The form saves the placeholder back: the secret stays.
	f.do(f.admin, "PATCH", "/api/sso", `{"client_secret":"`+secretPlaceholder+`","label":"Sign in with Contoso"}`)
	if oidc.LoadSecret(f.db) != "s3cr3t-value" {
		t.Error("saving the placeholder changed the secret")
	}
	if w := f.do(f.admin, "GET", "/api/sso", ""); strings.Contains(w.Body.String(), "s3cr3t-value") {
		t.Fatal("the secret reached the page")
	}
	for _, e := range activity.List(f.db, activity.Filter{}) {
		if strings.Contains(e.Detail, "s3cr3t-value") {
			t.Fatal("the secret reached the activity log")
		}
	}
}

func TestConnectedIdentitiesAreListedAndDisconnected(t *testing.T) {
	f := newSSOAdmin(t)
	id := oidc.Identity{Issuer: oidc.EntraIssuer(ssoTenant), Subject: "oid-pat", PreferredUsername: "pat@example.test"}
	oidc.SetLink(f.db, id, "bob", time.Now())
	var rows []map[string]any
	json.Unmarshal(f.do(f.admin, "GET", "/api/sso/links", "").Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["account"] != "bob" || rows[0]["shown_as"] != "pat@example.test" {
		t.Fatalf("links %+v", rows)
	}
	key := rows[0]["key"].(string)
	if w := f.do(f.bob, "DELETE", "/api/sso/links?key="+key, ""); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin disconnected: %d", w.Code)
	}
	if w := f.do(f.admin, "DELETE", "/api/sso/links?key="+key, ""); w.Code != 200 {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	if _, ok := oidc.LinkedAccount(f.db, id); ok {
		t.Error("still connected")
	}
	if w := f.do(f.admin, "DELETE", "/api/sso/links?key="+key, ""); w.Code != http.StatusNotFound {
		t.Errorf("a second disconnect: %d", w.Code)
	}
	if !strings.Contains(activity.List(f.db, activity.Filter{Kind: activity.Account})[0].Detail, "disconnected Microsoft sign-in pat@example.test from bob") {
		t.Error("not logged")
	}
}

func TestMicrosoftSignInIsAdminOnlyAndBesideSignIn(t *testing.T) {
	f := newSSOAdmin(t)
	for _, c := range []struct{ method, path string }{{"GET", "/api/sso"}, {"PATCH", "/api/sso"}, {"POST", "/api/sso/certificate"}, {"GET", "/api/sso/certificate.cer"}, {"GET", "/api/sso/links"}} {
		if w := f.do(f.bob, c.method, c.path, `{}`); w.Code != http.StatusForbidden {
			t.Errorf("a non-admin %s %s: %d", c.method, c.path, w.Code)
		}
	}
	WebBaseURL = func() string { return "" }
	if st := f.get(t); !strings.Contains(st["redirect_uri"].(string), "Set External URL first") {
		t.Errorf("no External URL: %q", st["redirect_uri"])
	}
	if sectionTab("Microsoft sign-in", "") != "People" || sectionRank["Microsoft sign-in"] != sectionRank["Sign-in"]+1 {
		t.Errorf("Microsoft sign-in is on %q at %d", sectionTab("Microsoft sign-in", ""), sectionRank["Microsoft sign-in"])
	}
	found := false
	for _, s := range (&AdminApp{}).systemSections() {
		found = found || s.Title == "Microsoft sign-in"
	}
	if !found {
		t.Error("the section is not on the admin page")
	}
}
