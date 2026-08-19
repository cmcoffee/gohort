package core

// The two halves against each other.
//
// peer_token_test.go proves the serving side in isolation; this drives a real
// consuming record through a real HTTP server, because the failures that matter
// here are the ones that only exist when both sides are running: a rotation
// that reaches some consumers and not others, a renewal storm the far side
// reads as a theft, and a fallback that quietly stops falling back.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// peerTokenServer stands up the real handlers over a scratch store and returns
// the base URL. Both sides share this process's RootDB, which is what lets a
// test assert on the serving side's records directly.
func peerTokenServer(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/v1/token", HandlePeerToken)
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// consumingPeer stores a peer record pointed at base, holding key.
func consumingPeer(t *testing.T, name, base, key string, useTokens bool) RemotePeer {
	t.Helper()
	p := RemotePeer{
		Name: name, BaseURL: base, Key: key,
		Caps: []string{PeerCapEmbeddings}, UseTokens: useTokens,
	}
	RootDB.Set(remotePeersTable, name, p)
	forgetPeerTokens(name)
	InvalidatePeerResolution()
	return p
}

// --- fallback ----------------------------------------------------------------

// TestAPeerNotOnTheTokenFlowPresentsItsStaticKey — the backwards-compatible
// path, and the one every existing pairing takes.
func TestAPeerNotOnTheTokenFlowPresentsItsStaticKey(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	k := grantFor(t)
	p := consumingPeer(t, "den", peerTokenServer(t), k.Key, false)

	if got := PeerCredential(p); got != k.Key {
		t.Errorf("PeerCredential = %q, want the static key — an existing pairing must not change behavior", got)
	}
}

// TestAnUnreachableTokenEndpointFallsBackToTheStaticKey — the failure that must
// not take a working link down. Nothing about the token flow is load-bearing
// while the static key is still accepted.
func TestAnUnreachableTokenEndpointFallsBackToTheStaticKey(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	k := grantFor(t)
	p := consumingPeer(t, "den", "http://127.0.0.1:1", k.Key, true)

	if got := PeerCredential(p); got != k.Key {
		t.Errorf("PeerCredential = %q, want the static key when exchange is impossible", got)
	}
}

// --- acquiring and spending --------------------------------------------------

func TestEnsurePeerTokenPairsAndThenPresentsTheAccessToken(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	k := grantFor(t)
	p := consumingPeer(t, "den", peerTokenServer(t), k.Key, true)

	if err := EnsurePeerToken(t.Context(), p); err != nil {
		t.Fatalf("pair: %v", err)
	}
	p, _ = GetRemotePeer("den")
	if p.AccessToken == "" || p.RefreshToken == "" {
		t.Fatal("the exchange did not persist a credential onto the record")
	}
	if p.Key != k.Key {
		t.Error("pairing replaced the static key — it must survive as the fallback and the pairing code")
	}
	if got := PeerCredential(p); got != p.AccessToken {
		t.Errorf("PeerCredential = %q, want the access token", got)
	}
	// And it authenticates on the serving side.
	if !authenticates(p.AccessToken) {
		t.Error("the acquired access token does not authenticate")
	}
}

// TestARestartResumesOnTheStoredCredential — every needless exchange is another
// family on the far side, and on a rotating grant the code that buys one is
// single use.
func TestARestartResumesOnTheStoredCredential(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	k := grantFor(t)
	p := consumingPeer(t, "den", peerTokenServer(t), k.Key, true)
	if err := EnsurePeerToken(t.Context(), p); err != nil {
		t.Fatalf("pair: %v", err)
	}
	p, _ = GetRemotePeer("den")
	want := p.AccessToken

	// Drop the in-memory view, as a restart would.
	forgetPeerTokens("den")
	if got := PeerCredential(p); got != want {
		t.Errorf("after a restart PeerCredential = %q, want the stored access token %q", got, want)
	}
}

// --- renewal -----------------------------------------------------------------

func TestEnsurePeerTokenRefreshesRatherThanRePairing(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	k := grantFor(t)
	p := consumingPeer(t, "den", peerTokenServer(t), k.Key, true)
	if err := EnsurePeerToken(t.Context(), p); err != nil {
		t.Fatalf("pair: %v", err)
	}
	p, _ = GetRemotePeer("den")
	first := p.AccessToken

	// Age it into the renewal lead.
	p.AccessExpires = time.Now().Add(peerRenewLead / 2).Format(time.RFC3339)
	RootDB.Set(remotePeersTable, "den", p)
	forgetPeerTokens("den")

	if err := EnsurePeerToken(t.Context(), p); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	p, _ = GetRemotePeer("den")
	if p.AccessToken == first {
		t.Error("the credential did not rotate")
	}
	// The pairing code must be untouched: spending it on an ordinary renewal
	// would burn the one credential that can re-establish the link.
	served, _ := peerGrantByID(k.ID)
	if served.Paired == "" {
		t.Error("precondition: the code should have been marked spent at pairing")
	}
	if _, code, _ := exchange(t, pairingBody(k.Key)); code == http.StatusOK {
		t.Error("the pairing code is still spendable — the refresh path re-paired instead of refreshing")
	}
}

// TestConcurrentRenewalsDoNotTripReuseDetection — THE bug this design exists to
// avoid. Several capabilities notice the expiry at once; without single-flight
// they present the same refresh token in parallel and the far side disables the
// grant, taking the link down on its own.
func TestConcurrentRenewalsDoNotTripReuseDetection(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	k := grantFor(t)
	p := consumingPeer(t, "den", peerTokenServer(t), k.Key, true)
	if err := EnsurePeerToken(t.Context(), p); err != nil {
		t.Fatalf("pair: %v", err)
	}
	p, _ = GetRemotePeer("den")
	p.AccessExpires = time.Now().Add(-time.Second).Format(time.RFC3339)
	RootDB.Set(remotePeersTable, "den", p)
	forgetPeerTokens("den")
	p, _ = GetRemotePeer("den")

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); PeerCredential(p) }()
	}
	wg.Wait()
	// PeerCredential renews in the background; give the single flight a moment
	// to finish before judging the outcome.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := peerGrantByID(k.ID); got.Disabled {
			break
		}
		if cur := loadPeerTokens("den"); cur.Access != "" && time.Now().Before(cur.Expires) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, _ := peerGrantByID(k.ID); got.Disabled {
		t.Fatal("concurrent renewals disabled the grant — the consuming side manufactured a theft report")
	}
	cur := loadPeerTokens("den")
	if cur.Access == "" || !time.Now().Before(cur.Expires) {
		t.Error("the concurrent renewals produced no usable credential")
	}
}

// --- revocation --------------------------------------------------------------

// TestARevokedGrantClearsTheDeadPair — a refresh loop against a grant that is
// gone is noise; falling back to the static key makes the failure one clear 401.
func TestARevokedGrantClearsTheDeadPair(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	k := grantFor(t)
	p := consumingPeer(t, "den", peerTokenServer(t), k.Key, true)
	if err := EnsurePeerToken(t.Context(), p); err != nil {
		t.Fatalf("pair: %v", err)
	}
	SetPeerKeyDisabled(k.ID, true)

	p, _ = GetRemotePeer("den")
	p.AccessExpires = time.Now().Add(-time.Second).Format(time.RFC3339)
	RootDB.Set(remotePeersTable, "den", p)
	forgetPeerTokens("den")
	p, _ = GetRemotePeer("den")

	if err := EnsurePeerToken(t.Context(), p); err == nil {
		t.Fatal("refreshing against a revoked grant reported success")
	}
	p, _ = GetRemotePeer("den")
	if p.RefreshToken != "" {
		t.Error("the dead refresh token was kept — the next renewal will loop on it")
	}
	if got := PeerCredential(p); got != k.Key {
		t.Errorf("after revocation PeerCredential = %q, want the static key", got)
	}
}

// --- adoption ----------------------------------------------------------------

// TestTheFlowIsAdoptedOnlyWhenTheFarSideRequiresIt — latching on for any peer
// that merely OFFERS exchange would have every existing pairing start rotating
// on its next refresh, which is the flag day the opt-in exists to avoid.
func TestTheFlowIsAdoptedOnlyWhenTheFarSideRequiresIt(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads

	offered := PeerManifest{Token: &PeerTokenInfo{Path: "/api/peer/v1/token", Required: false}}
	required := PeerManifest{Token: &PeerTokenInfo{Path: "/api/peer/v1/token", Required: true}}
	silent := PeerManifest{}

	if adoptPeerTokenFlow(RemotePeer{Name: "a"}, offered) {
		t.Error("a peer that merely offers exchange switched an existing pairing over")
	}
	if adoptPeerTokenFlow(RemotePeer{Name: "a"}, silent) {
		t.Error("a manifest that says nothing switched a pairing over")
	}
	if !adoptPeerTokenFlow(RemotePeer{Name: "a"}, required) {
		t.Error("a peer that REQUIRES exchange did not switch the pairing over")
	}
	// An operator who turned it on by hand keeps it on: "not required" means the
	// static key would also work, not that tokens have stopped working.
	if !adoptPeerTokenFlow(RemotePeer{Name: "a", UseTokens: true}, offered) {
		t.Error("a refresh silently downgraded a link the operator hardened")
	}
}

// --- the image credential ----------------------------------------------------

// TestRotationRepublishesTheImageCredential — the one consumer that cannot
// resolve at read time. Its key lives in a managed SecureCredential the
// generated connectors name, so a rotation that updated only the peer record
// would leave image generation holding a dead token while everything else works.
func TestRotationRepublishesTheImageCredential(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	prevAuth := AuthDB
	AuthDB = func() Database { return RootDB }
	defer func() { AuthDB = prevAuth }()

	p := consumingPeer(t, "den", "https://den.example", "static-key", true)
	p.ImageConnectors = []string{"peer-den-sd"}
	RootDB.Set(remotePeersTable, "den", p)

	credName := peerCredentialName("den")
	if err := Secure().Save(SecureCredential{
		Name: credName, Type: SecureCredHeader, ParamName: peerKeyHeader,
		BaseURL: p.BaseURL, AllowedURLPattern: imageHostPattern(p.BaseURL),
		Secured: true, Managed: "peer",
	}, "static-key"); err != nil {
		t.Skipf("secure store unavailable in this configuration: %v", err)
	}

	storePeerTokens("den", peerTokens{
		Access: "rotated-access", Refresh: "rotated-refresh",
		Expires: time.Now().Add(peerAccessTTL),
	})

	got, ok := Secure().loadSecret(credName)
	if !ok {
		t.Fatal("the republished credential has no secret")
	}
	if got != "rotated-access" {
		t.Errorf("the image credential still holds %q — image generation would 401 while "+
			"every other capability kept working", got)
	}
}

// --- shape -------------------------------------------------------------------

// TestPeerCredentialNeverBlocks — it sits under GetTranscribeConfig and
// LoadWebSearchConfig, which are called to answer questions as small as "is
// transcription enabled". A round trip there makes a page render wait on
// another machine.
func TestPeerCredentialNeverBlocks(t *testing.T) {
	defer scratchPeerStore(t)()
	defer waitPeerTokenIdle() // runs FIRST: no renewal may outlive the store it reads
	// An address nothing answers on. PeerCredential must return the fallback
	// without waiting to find that out.
	p := consumingPeer(t, "den", "http://127.0.0.1:1", "static-key", true)

	done := make(chan string, 1)
	go func() { done <- PeerCredential(p) }()
	select {
	case got := <-done:
		if got != "static-key" {
			t.Errorf("PeerCredential = %q, want the static key", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PeerCredential blocked on the network")
	}
}
