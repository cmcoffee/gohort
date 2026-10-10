package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A session cookie issued before the rename is still read, so an upgrade
// signs nobody out; the new name wins when both are present.
func TestTheOldSessionCookieIsStillRead(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: legacy_cookie_name, Value: "old"})
	if c, err := authCookie(r); err != nil || c.Value != "old" {
		t.Fatalf("legacy cookie not read: %v %v", c, err)
	}
	r.AddCookie(&http.Cookie{Name: auth_cookie_name, Value: "new"})
	if c, _ := authCookie(r); c.Value != "new" {
		t.Errorf("the current name should win, got %q", c.Value)
	}
}
