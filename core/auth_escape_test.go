package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// Nothing a URL carries reaches an auth page as markup: a reset link's token,
// an app path in "access denied" or "app unavailable".
func TestAuthPagesDoNotReflectMarkup(t *testing.T) {
	const evil = `"><script>alert(1)</script>`
	db := &DBase{Store: kvlite.MemStore()}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		ResetHandler(db)(w, httptest.NewRequest(method, "/reset?token="+strings.ReplaceAll(evil, " ", "%20"), nil))
		if strings.Contains(w.Body.String(), "<script>") {
			t.Errorf("%s /reset reflected the token: %s", method, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	serveResetPage(w, evil, "")
	if strings.Contains(w.Body.String(), "<script>") || strings.Contains(w.Body.String(), `"><`) {
		t.Errorf("the reset form wrote the token raw: %s", w.Body.String())
	}

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	w = httptest.NewRecorder()
	writeForbidden(w, r, `<svg onload=alert(1)>`)
	if strings.Contains(w.Body.String(), "<svg") {
		t.Errorf("access denied reflected the path: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	writeAppDisabled(w, r, `<svg onload=alert(1)>`)
	if strings.Contains(w.Body.String(), "<svg") {
		t.Errorf("app unavailable reflected the path: %s", w.Body.String())
	}
}
