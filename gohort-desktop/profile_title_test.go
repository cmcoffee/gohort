package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// The window's title is the desktop profile's name the server gives for the
// signed-in person, "" when none applies, and not known while the server
// will not say (not signed in, unreachable), so the title is left as it is.
func TestTheWindowTakesTheProfilesName(t *testing.T) {
	reply, status := `{"name":"Front Desk","start_path":"/apps/desk/"}`, http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/desktop/profile" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	defer srv.Close()
	t.Setenv(core.ENV_GOHORT_ADDR, srv.URL)
	a := &App{config: &core.Config{}}

	if name, ok := a.profileName(); !ok || name != "Front Desk" {
		t.Errorf("with a profile: %q %v", name, ok)
	}
	reply = `{}`
	if name, ok := a.profileName(); !ok || name != "" {
		t.Errorf("with none: %q %v", name, ok)
	}
	status = http.StatusUnauthorized
	if _, ok := a.profileName(); ok {
		t.Error("a server that would not say was taken as an answer")
	}
}
