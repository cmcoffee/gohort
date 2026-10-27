package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The desktop key is the user's way into their desktop machine's tools, so a
// GET never mints it or hands it out. A GET is what any same-origin read can
// issue with the viewer's cookies: a live artifact's data bridge did, and a
// prompt injection used it to read the key.
func TestTheDesktopKeyIsNeverServedOnAGet(t *testing.T) {
	db := revokeFixture(t, "alice")
	cookie := &http.Cookie{Name: auth_cookie_name, Value: AuthCreateSession(db, "alice")}

	get := httptest.NewRequest(http.MethodGet, "/api/desktop/key", nil)
	get.AddCookie(cookie)
	rec := httptest.NewRecorder()
	HandleDesktopKey(rec, get)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d, want 405", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"key"`) {
		t.Errorf("GET returned a key: %s", rec.Body.String())
	}
	if _, ok := desktopKeyForUser("alice"); ok {
		t.Error("GET minted a key")
	}

	post := httptest.NewRequest(http.MethodPost, "/api/desktop/key", nil)
	post.AddCookie(cookie)
	rec = httptest.NewRecorder()
	HandleDesktopKey(rec, post)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"key"`) {
		t.Errorf("POST should mint and return the key, got %d %s", rec.Code, rec.Body.String())
	}
}
