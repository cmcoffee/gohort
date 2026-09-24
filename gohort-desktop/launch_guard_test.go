package main

// The local proxy serves only its own window: the launch secret sets a strict
// cookie once, every later request needs it, and the Host must be loopback.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheLocalProxyAnswersOnlyItsWindow(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("proxied")) })
	h := guardLaunch(inner, "s3cret", 4242)
	do := func(path, host string, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = host
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: launchCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("/", "127.0.0.1:4242", ""); w.Code != http.StatusForbidden {
		t.Errorf("no cookie: %d", w.Code)
	}
	if w := do("/", "evil.example:4242", "s3cret"); w.Code != http.StatusForbidden {
		t.Errorf("a rebound hostname got through: %d", w.Code)
	}
	if w := do(launchBootPath+"?t=wrong&next=/", "127.0.0.1:4242", ""); w.Code != http.StatusForbidden {
		t.Errorf("a wrong boot secret: %d", w.Code)
	}
	// Boot answers with a page that navigates on, never a redirect: the window
	// arrives from the wails:// page, a different site, and a redirect at the end
	// of that navigation is cross-site too, so the strict cookie would not ride
	// it and the first real page was refused.
	w := do(launchBootPath+"?t=s3cret&next=%2Fapps%2Fx", "127.0.0.1:4242", "")
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" || !strings.Contains(w.Header().Get("Set-Cookie"), "SameSite=Strict") {
		t.Fatalf("boot: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Body.String(), `location.replace("/apps/x")`) {
		t.Errorf("boot does not navigate on to the requested page: %s", w.Body.String())
	}
	if w := do(launchBootPath+"?t=s3cret&next=%2F%2Fevil.example", "127.0.0.1:4242", ""); !strings.Contains(w.Body.String(), `location.replace("/")`) {
		t.Errorf("boot navigated off-site: %s", w.Body.String())
	}
	if w := do(launchBootPath+"?t=s3cret&next=%2F%3C%2Fscript%3E", "127.0.0.1:4242", ""); strings.Contains(w.Body.String(), "/</script>") {
		t.Errorf("a next path closed the script tag: %s", w.Body.String())
	}
	if w := do("/", "127.0.0.1:4242", "s3cret"); w.Code != http.StatusOK || w.Body.String() != "proxied" {
		t.Errorf("the window was refused: %d", w.Code)
	}
}
