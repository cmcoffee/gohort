package core

// How the deployment-wide API key may be presented.
//
// The key is a blanket authentication bypass, so where it travels matters: in
// a URL it reaches browser history, Referer headers on any outbound link, and
// the log of every proxy in between. The header has none of that, so the URL
// spelling is off unless a deployment says otherwise.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// keyAuthFixture wires a deployment key and returns the middleware in front of
// a handler that records whether it was reached.
func keyAuthFixture(t *testing.T, allowQuery bool) (http.Handler, *bool) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}

	prevKey, prevAllow := AuthAPIKey, AuthAPIKeyAllowQuery
	AuthAPIKey = func() string { return "s3cret" }
	AuthAPIKeyAllowQuery = func() bool { return allowQuery }
	t.Cleanup(func() { AuthAPIKey, AuthAPIKeyAllowQuery = prevKey, prevAllow })

	// A configured user, so the middleware does not fall through its
	// "no users yet" pass-through and prove nothing. Written directly: this
	// test is about how a key may be presented, not about account creation.
	db.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})

	reached := new(bool)
	h := AuthMiddleware(db, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	}))
	return h, reached
}

func TestTheKeyIsAcceptedInItsHeader(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	r := httptest.NewRequest("GET", "/anything", nil)
	r.Header.Set("X-Gohort-Key", "s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !*reached || w.Code != 200 {
		t.Fatalf("the header form was refused: %d %s", w.Code, w.Body.String())
	}
}

// The default. A caller still using the URL is told the whole answer: what was
// wrong, what to send instead, and the setting that puts it back.
func TestTheKeyInAURLIsRefusedAndSaysWhy(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=s3cret", nil))

	if *reached {
		t.Fatal("the URL form was accepted while it is off")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"X-Gohort-Key", "?key=", "setup menu"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, body)
		}
	}
	// It must never hand the key back in its own complaint.
	if strings.Contains(body, "s3cret") {
		t.Error("the refusal echoed the key")
	}
}

// A deployment that has said so keeps the old spelling working.
func TestTheURLFormWorksWhenTurnedOn(t *testing.T) {
	h, reached := keyAuthFixture(t, true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=s3cret", nil))
	if !*reached || w.Code != 200 {
		t.Fatalf("the URL form was refused while it is on: %d %s", w.Code, w.Body.String())
	}
}

// Unset reads as off. A deployment that has never configured this, or one
// whose setting failed to load, must not have the permissive answer.
func TestAnUnsetSettingReadsAsOff(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	prev := AuthAPIKeyAllowQuery
	AuthAPIKeyAllowQuery = nil
	t.Cleanup(func() { AuthAPIKeyAllowQuery = prev })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=s3cret", nil))
	if *reached {
		t.Fatal("an unset setting allowed the URL form")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// A WRONG key in a URL is answered the way a wrong key anywhere else is, not
// with the refusal above. Telling an attacker which spelling to stop guessing
// at is information the refusal should not give away.
func TestAWrongKeyInAURLIsNotToldAboutTheHeader(t *testing.T) {
	h, reached := keyAuthFixture(t, false)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/anything?key=wrong", nil))
	if *reached {
		t.Fatal("a wrong key was accepted")
	}
	if strings.Contains(w.Body.String(), "X-Gohort-Key") {
		t.Error("a wrong key was told how the right one should be presented")
	}
}
