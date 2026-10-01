package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A key may run only so many of one capability at once, and pressing Disable
// stops what it already has running.
func TestAPeerKeysWorkIsBoundedAndStopsWithTheKey(t *testing.T) {
	peerTestDB(t)
	k, err := MintPeerKey("den", []string{PeerCapInvestigate}, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	var held []*http.Request
	for i := 0; i < peerConcurrency[PeerCapInvestigate]; i++ {
		hr, release, ok := peerHold(httptest.NewRecorder(), r, k, PeerCapInvestigate)
		if !ok {
			t.Fatalf("request %d refused under the cap", i+1)
		}
		defer release()
		held = append(held, hr)
	}
	w := httptest.NewRecorder()
	if _, _, ok := peerHold(w, r, k, PeerCapInvestigate); ok || w.Code != http.StatusTooManyRequests {
		t.Errorf("a request past the cap: ok=%v code=%d", ok, w.Code)
	}
	SetPeerKeyDisabled(k.ID, true)
	for i, hr := range held {
		if hr.Context().Err() != context.Canceled {
			t.Errorf("request %d kept running after its key was disabled", i+1)
		}
	}
}

// A peer generation is held to a bounded length and one choice; a request
// already inside the bounds passes through as sent.
func TestAPeersModelRequestIsBounded(t *testing.T) {
	in := []byte(`{"model":"m","max_tokens":1000000,"n":8,"messages":[]}`)
	out := string(clampPeerModelBody(in))
	if !strings.Contains(out, `"max_tokens":16384`) || !strings.Contains(out, `"n":1`) {
		t.Errorf("not clamped: %s", out)
	}
	ok := []byte(`{"model":"m","max_tokens":512}`)
	if string(clampPeerModelBody(ok)) != string(ok) {
		t.Error("a request inside the bounds was rewritten")
	}
}

// The peer's credential goes to the peer's address only: a redirect, or any
// request built for another host, is refused rather than sent with it. A
// response past the size bound fails instead of being read whole.
func TestAPeersCredentialStaysAtItsAddress(t *testing.T) {
	peerTestDB(t)
	var leaked string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") + r.Header.Get(peerKeyHeader)
	}))
	defer elsewhere.Close()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
	}))
	defer peer.Close()
	tr := &peerTransport{name: "den", held: RemotePeer{Name: "den", BaseURL: peer.URL, Key: "the-key"}}
	_, err := (&http.Client{Transport: tr}).Get(peer.URL + "/api/peer/v1/x")
	if err == nil || !strings.Contains(err.Error(), "not the peer's address") {
		t.Errorf("the redirect was followed: %v", err)
	}
	if leaked != "" {
		t.Errorf("the key reached another host: %q", leaked)
	}
	u, _ := url.Parse("https://den.example:443/x")
	if !peerSameOrigin("https://den.example", u) {
		t.Error("the default port is the same address")
	}
	u, _ = url.Parse("http://den.example/x")
	if peerSameOrigin("https://den.example", u) {
		t.Error("a downgrade to http is the same address")
	}

	resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 10)))}
	capPeerBody(resp)
	resp.Body.(*cappedBody).left = 4
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("a body past the bound was read whole")
	}
}

// A key issued by an account that has been deleted authenticates nobody, and
// deleting the account disables it.
func TestAPeerKeyEndsWithItsAccount(t *testing.T) {
	peerTestDB(t)
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })

	k, _ := MintPeerKey("den", []string{PeerCapInvestigate}, 0)
	if _, err := SetPeerKeyScope(k.ID, "alice", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := LookupPeerKey(k.Key); !ok {
		t.Fatal("alice's key does not work while she exists")
	}
	adb.Unset(AuthTable, "user:alice")
	if _, ok := LookupPeerKey(k.Key); ok {
		t.Error("the key of a removed account still authenticates")
	}
	if got := RevokeUserCredentials(adb, "alice"); got["peer keys"] != 1 {
		t.Errorf("deleting the account did not disable the key: %v", got)
	}
}

// Behind a trusted proxy each caller is counted at its own address, so one
// failing source cannot use up everyone's allowance; a client cannot name its
// own address from outside.
func TestRequestSourceSeesThroughATrustedProxyOnly(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := RequestSource(r); got != "203.0.113.9" {
		t.Errorf("behind the local proxy: %q", got)
	}
	r.RemoteAddr = "198.51.100.7:5000"
	if got := RequestSource(r); got != "198.51.100.7" {
		t.Errorf("a direct caller named its own address: %q", got)
	}
}
