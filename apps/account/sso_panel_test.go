package account

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/clientcert"
	"github.com/cmcoffee/gohort/core/oidc"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Account page offers Microsoft sign-in while the deployment does, and
// keeps the panel while the account still has an identity connected, so it
// can be disconnected after sign-in is turned off.
func TestTheAccountPageOffersMicrosoftSignInWhenItIsOn(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(db, "pat", "pw-pat-123", false)
	token := AuthCreateSession(db, "pat")
	page := func() string {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		(&Account{}).servePage(w, r)
		return w.Body.String()
	}
	if strings.Contains(page(), "acct-sso") {
		t.Fatal("a panel while sign-in is off and nothing is connected")
	}
	cert, _ := clientcert.Generate("test", time.Hour)
	oidc.SaveCertificate(db, cert)
	if err := oidc.SaveSettings(db, oidc.Settings{Enabled: true, Tenant: "11111111-2222-3333-4444-555555555555", ClientID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}); err != nil {
		t.Fatal(err)
	}
	if p := page(); !strings.Contains(p, "acct-sso") || !strings.Contains(p, "/api/sso/connect") {
		t.Fatal("no panel while sign-in is on")
	}
	s := oidc.LoadSettings(db)
	s.Enabled = false
	oidc.SaveSettings(db, s)
	oidc.SetLink(db, oidc.Identity{Issuer: "https://issuer.example.test", Subject: "oid-pat", PreferredUsername: "pat@example.test"}, "pat", time.Now())
	if !strings.Contains(page(), "acct-sso") {
		t.Error("no panel to disconnect an identity while sign-in is off")
	}
}
