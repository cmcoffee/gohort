package extensions

// The Extensions page renders: a signed-out visit is refused, and a signed-in
// one gets the page with its four places, the Connected accounts card, and
// the client actions its tables call. Before this, nothing in the package
// rendered the page at all; the layout tests read its source.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheExtensionsPageRenders(t *testing.T) {
	app, req := authedExtensions(t)

	anon := httptest.NewRecorder()
	app.servePage(anon, httptest.NewRequest(http.MethodGet, "/extensions", nil))
	if anon.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", anon.Code)
	}

	w := httptest.NewRecorder()
	app.servePage(w, req(http.MethodGet, "/extensions"))
	if w.Code != http.StatusOK {
		t.Fatalf("signed in: %d, want 200", w.Code)
	}
	page := w.Body.String()
	for _, place := range []string{`"title":"APIs"`, `"title":"Connected accounts"`, `"title":"Tools"`, `"title":"Skills"`} {
		if !strings.Contains(page, place) {
			t.Errorf("the page is missing %s", place)
		}
	}
	// The card's HTML rides inside the page's JSON config, quotes escaped.
	if !strings.Contains(page, `id=\"acct-conns\"`) {
		t.Error("Connected accounts has no card to set a key in")
	}
	// Registered, not only named: a table's row action names the same
	// action, and a page that names it without registering it does nothing.
	for _, action := range []string{"tool_security", "category_security", "export_tool", "export_skill", "extensions_import"} {
		if !strings.Contains(page, `uiRegisterClientAction("`+action+`"`) {
			t.Errorf("the page does not register the %s action its tables call", action)
		}
	}
}
