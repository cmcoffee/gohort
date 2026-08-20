// An internal call has to prove it is one.
//
// The auth bypass for inter-app calls used to infer it: loopback TCP peer, no
// X-Forwarded-For, does not look like a browser. The middle condition is a
// property of the operator's reverse proxy rather than of the request — nginx
// adds no such header on its own, so behind a bare proxy_pass every request in
// the world arrived on loopback with nothing to disqualify it, and any client
// that did not look like a browser skipped authentication entirely. These tests
// pin the replacement: a per-process token that no proxy configuration can
// produce, plus a genuinely local peer.
package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// local marks a request as arriving on the loopback interface.
func local(r *http.Request) *http.Request {
	r.RemoteAddr = "127.0.0.1:44444"
	return r
}

func TestInternalRequestCarriesItsProof(t *testing.T) {
	WebListenAddr = "127.0.0.1:8181"
	req, err := NewInternalRequest(t.Context(), http.MethodGet, "/blogger/api/keywords", nil)
	if err != nil {
		t.Fatalf("building an internal request: %v", err)
	}
	if got := req.Header.Get(internalAuthHeader); got == "" {
		t.Fatal("an internal request went out with no proof of origin")
	}
	if !IsInternalRequest(local(req)) {
		t.Error("a request this process built was not recognized as internal")
	}
}

func TestBareLoopbackIsNotInternal(t *testing.T) {
	// The finding. Behind a reverse proxy that adds no forwarding header,
	// this is what every request from anywhere looks like.
	r := local(httptest.NewRequest(http.MethodGet, "/admin/api/users", nil))
	if IsInternalRequest(r) {
		t.Fatal("a plain loopback request still counts as internal")
	}
}

func TestForgedProofIsRejected(t *testing.T) {
	for _, token := range []string{"", "guess", internalAuthToken + "x", "0"} {
		r := local(httptest.NewRequest(http.MethodGet, "/admin/api/users", nil))
		r.Header.Set(internalAuthHeader, token)
		if IsInternalRequest(r) {
			t.Errorf("token %q was accepted", token)
		}
	}
}

func TestProofFromOffBoxIsRejected(t *testing.T) {
	// Both halves are required, so a token that somehow leaked is still no
	// use from another machine.
	r := httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	r.RemoteAddr = "203.0.113.9:44444"
	r.Header.Set(internalAuthHeader, internalAuthToken)
	if IsInternalRequest(r) {
		t.Fatal("a valid token was honored from a remote peer")
	}
}

func TestProofWithAForwardingHeaderIsRejected(t *testing.T) {
	// A request that reached us through a proxy is not one we made, whatever
	// it carries — this is what stops a token replayed through the operator's
	// own front end.
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		r := local(httptest.NewRequest(http.MethodGet, "/admin/api/users", nil))
		r.Header.Set(internalAuthHeader, internalAuthToken)
		r.Header.Set(h, "203.0.113.9")
		if IsInternalRequest(r) {
			t.Errorf("accepted a proxied request carrying %s", h)
		}
	}
}

func TestTokenIsNotGuessableOrShared(t *testing.T) {
	if len(internalAuthToken) < 32 {
		t.Errorf("the internal token is too short to be unguessable: %d chars", len(internalAuthToken))
	}
	// Never travels except on requests this process builds, so it must not
	// turn up anywhere a caller could read it back.
	r := local(httptest.NewRequest(http.MethodGet, "/", nil))
	if IsInternalRequest(r) {
		t.Error("an ordinary request was treated as internal")
	}
}
