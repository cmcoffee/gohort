package extensions

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/userprofile"
)

// A user profile that takes connected accounts away leaves them off the
// Extensions page; /account/api/connections refuses them as well.
func TestAProfileTakesConnectedAccountsOffTheExtensionsPage(t *testing.T) {
	app, req := authedExtensions(t)
	serve(app)
	userprofile.SetStore(RootDB)
	t.Cleanup(func() { userprofile.SetStore(nil) })
	page := func() string {
		w := httptest.NewRecorder()
		app.servePage(w, req(http.MethodGet, "/"))
		return w.Body.String()
	}
	if !strings.Contains(page(), `"title":"Connected accounts"`) {
		t.Fatal("no Connected accounts before any profile")
	}
	if err := userprofile.Save(userprofile.Profile{ID: "p", Name: "Focused", Users: []string{"alice"}, Removes: []string{userprofile.RemoveConnectedAccounts}}); err != nil {
		t.Fatal(err)
	}
	if p := page(); strings.Contains(p, `"title":"Connected accounts"`) || !strings.Contains(p, `"title":"Tools"`) {
		t.Error("Connected accounts is still on the page, or the page lost more than it")
	}
}
