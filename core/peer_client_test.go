package core

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The transport is the encapsulation: a caller builds an ordinary request and
// the credential — including recovering from a refused one — is not its
// problem. These test that directly rather than through any one capability,
// because the point of moving it here was that it stops being per-capability.

// A request sent through the transport authenticates itself. The caller sets no
// key at all, and one that sets a stale one has it replaced.
func TestPeerTransportAuthenticatesWithoutTheCallerSupplyingAKey(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	p, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	body := []byte(`{"input":["hello"]}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.EmbeddingsURL()+"/embeddings", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// A stale credential of exactly the kind a saved config carries.
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := PeerHTTPClient("gpu-box", 20*time.Second).Do(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the transport did not replace the caller's stale key", resp.StatusCode)
	}
}

// The recovery, at the transport rather than in a capability: a token this side
// believes in and the peer has dropped. Nothing else reaches this state —
// EnsurePeerToken sees a token inside its life and declines to renew — so a 401
// is the only evidence, and acting on it has to live where every capability
// gets it.
//
// The body matters. The retry re-sends it, so a transport that replayed a
// consumed reader would turn a recoverable 401 into a mystifying empty request.
func TestPeerTransportRepairsARefusedCredentialAndReplaysTheBody(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	p, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	live := loadPeerTokens("gpu-box")
	if live.Access == "" || live.Refresh == "" {
		t.Fatal("precondition: pairing should have produced both tokens")
	}
	dead := live
	dead.Access = "dead-" + live.Access
	dead.Expires = time.Now().Add(time.Hour) // our clock says it is fine
	storePeerTokens("gpu-box", dead)

	body := []byte(`{"input":["a body that has to survive the retry"]}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.EmbeddingsURL()+"/embeddings", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := PeerHTTPClient("gpu-box", 20*time.Second).Do(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a refused credential was not repaired", resp.StatusCode)
	}
	if got := PeerCredential(mustPeer(t, "gpu-box")); got == dead.Access {
		t.Error("the refused token is still installed — every later request fails the same way")
	} else if _, ok := peerKeyFromAccessToken(got); !ok {
		t.Errorf("expected a live access token after repair, got %q", got)
	}
	// The refresh token has to survive: taking it would leave a spent pairing
	// code and an operator re-pairing by hand.
	if loadPeerTokens("gpu-box").Refresh == "" {
		t.Error("repair discarded the refresh token, leaving nothing to renew with")
	}
}

// A capability configured against something that is NOT a peer must get a plain
// client. Embeddings point at Ollama or llama.cpp far more often than at a
// peer, and a transport that attached peer credentials to those would be
// sending a secret to a third party.
func TestPeerClientForProviderLeavesNonPeerProvidersAlone(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer local-key")
	resp, err := PeerClientForProvider("local", 10*time.Second).Do(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer resp.Body.Close()
	if sawAuth != "Bearer local-key" {
		t.Errorf("a non-peer provider had its Authorization rewritten to %q", sawAuth)
	}
}

// The LLM tiers cannot ride the peer transport — they go out on snugforge's
// APIClient, which has an AuthFunc hook but no transport hook — so the same
// recovery is expressed in the retry wrapper. This is that recovery: a peer
// answering 401 must drop the credential it refused, so the next attempt
// exchanges a fresh one instead of presenting the dead token forever.
func TestPeerBackedModelDropsARefusedCredential(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	live := loadPeerTokens("gpu-box")
	dead := live
	dead.Access = "dead-" + live.Access
	dead.Expires = time.Now().Add(time.Hour) // our clock still trusts it
	storePeerTokens("gpu-box", dead)

	r := &retryLLM{peer: "gpu-box"}

	// Anything that is not a 401 leaves the credential alone: a 500 from the
	// far side is the peer's problem, not the credential's, and re-pairing on
	// one would spend a token family over a transient fault.
	if r.peerRefused(&APIError{StatusCode: 500, Message: "boom", Provider: "peer"}) {
		t.Error("a 500 was treated as a credential refusal")
	}
	if loadPeerTokens("gpu-box").Access != dead.Access {
		t.Error("a non-401 error dropped the credential anyway")
	}

	if !r.peerRefused(&APIError{StatusCode: 401, Message: "unrecognized or disabled peer key", Provider: "peer"}) {
		t.Fatal("a 401 from a peer-backed tier was not recognized as a refusal")
	}
	if loadPeerTokens("gpu-box").Access == dead.Access {
		t.Error("the refused token is still installed — every later model call fails the same way")
	}
	// The refresh token has to survive, or recovery means re-pairing by hand.
	if loadPeerTokens("gpu-box").Refresh == "" {
		t.Error("the refusal discarded the refresh token, leaving nothing to renew with")
	}

	// A tier that is NOT peer-backed must never touch peer credentials on a
	// 401 — that is an ordinary auth failure against OpenAI or a local server.
	plain := &retryLLM{}
	if plain.peerRefused(&APIError{StatusCode: 401, Message: "invalid api key", Provider: "openai"}) {
		t.Error("a non-peer tier treated its own 401 as a peer credential refusal")
	}
}
