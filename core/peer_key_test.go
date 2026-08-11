package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// peerTestDB points RootDB at a fresh store and restores it afterwards, since
// the peer key store is process-global.
func peerTestDB(t *testing.T) {
	t.Helper()
	prev := RootDB
	t.Cleanup(func() { RootDB = prev })
	RootDB = &DBase{Store: kvlite.MemStore()}
}

// A peer key must never satisfy ordinary user authentication. The tempting
// implementation registers it with RegisterAPIKeyValidator next to the desktop
// key — and a validator there returns a USERNAME, which userFromAPIKey turns
// into that user everywhere a request is authenticated. "You may use my GPU"
// would silently become "you may read my mail".
//
// This pins the separation at the seam where it would be lost.
func TestPeerKeyIsNotAUserCredential(t *testing.T) {
	peerTestDB(t)
	pk, err := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	r.Header.Set("X-API-Key", pk.Key)
	if user := APIKeyUser(r); user != "" {
		t.Fatalf("a peer key resolved to user %q — it must not authenticate as a person", user)
	}
	// And it still works as a PEER credential, which is the whole point.
	r2 := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r2.Header.Set(peerKeyHeader, pk.Key)
	if _, ok := peerFromRequest(r2); !ok {
		t.Error("the peer key did not authenticate as a peer")
	}
}

// Capabilities are an allowlist. A key granted embeddings must not gain image
// rendering the day image sharing ships.
func TestPeerKeyCapabilitiesAreAnAllowlist(t *testing.T) {
	peerTestDB(t)
	pk, err := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !pk.Allows(PeerCapEmbeddings) {
		t.Error("granted capability not allowed")
	}
	for _, c := range []string{PeerCapImages, PeerCapModels, PeerCapTranscode} {
		if pk.Allows(c) {
			t.Errorf("ungranted capability %q was allowed", c)
		}
	}
}

// A typo'd capability is rejected at mint. Stored as-is it would produce a key
// that authenticates fine and is refused by every handler — which reads as a
// broken key rather than a misspelled grant.
func TestMintRejectsUnknownCapability(t *testing.T) {
	peerTestDB(t)
	if _, err := MintPeerKey("mac", []string{"embedding"}, 0); err == nil {
		t.Fatal("a misspelled capability should be rejected at mint")
	} else if !strings.Contains(err.Error(), "embeddings") {
		t.Errorf("the error should name the valid capabilities, got: %v", err)
	}
	if _, err := MintPeerKey("mac", nil, 0); err == nil {
		t.Error("a key granting nothing should be rejected")
	}
}

// Revocation has to take effect at lookup, not merely at each call site — a
// disabled key that still resolves is one forgotten check away from working.
func TestDisabledPeerKeyStopsResolving(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	if _, ok := LookupPeerKey(pk.Key); !ok {
		t.Fatal("fresh key should resolve")
	}
	if !SetPeerKeyDisabled(pk.ID, true) {
		t.Fatal("disable failed")
	}
	if _, ok := LookupPeerKey(pk.Key); ok {
		t.Error("a disabled key still resolves")
	}
	// Belt and braces: even holding the record, Allows says no.
	disabled := PeerKey{Caps: []string{PeerCapEmbeddings}, Disabled: true}
	if disabled.Allows(PeerCapEmbeddings) {
		t.Error("a disabled key allows a granted capability")
	}
}

// An unauthenticated caller gets 401 and no manifest — the capability list is
// itself information about the host.
func TestPeerManifestRequiresAKey(t *testing.T) {
	peerTestDB(t)
	w := httptest.NewRecorder()
	HandlePeerManifest(w, httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no key → %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), PeerCapEmbeddings) {
		t.Error("the manifest leaked capability names to an unauthenticated caller")
	}
}

// The manifest distinguishes "not built yet" from "not granted to you", so a
// peer debugging its config does not have to guess which wall it hit.
func TestPeerManifestSeparatesServedFromGranted(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerManifest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest → %d: %s", w.Code, w.Body.String())
	}
	var m PeerManifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]PeerManifestEntry{}
	for _, e := range m.Capabilities {
		seen[e.Name] = e
	}
	if len(seen) != len(PeerCapabilities()) {
		t.Errorf("manifest listed %d capabilities, want all %d", len(seen), len(PeerCapabilities()))
	}
	if e := seen[PeerCapEmbeddings]; !e.Served || !e.Granted {
		t.Errorf("embeddings should be served AND granted: %+v", e)
	}
	if e := seen[PeerCapImages]; e.Served {
		t.Errorf("images is not implemented yet but reported served: %+v", e)
	}
	if e := seen[PeerCapImages]; e.Granted {
		t.Errorf("images was not granted to this key but reported granted: %+v", e)
	}
}

// A key without the capability is refused, and the refusal names what it DOES
// grant — otherwise a peer pointed at the wrong key cannot tell that from a
// capability the host does not offer.
func TestPeerEmbeddingsRefusesUngrantedKey(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapImages}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings",
		strings.NewReader(`{"input":["hello"]}`))
	r.Header.Set("Authorization", "Bearer "+pk.Key)
	w := httptest.NewRecorder()
	HandlePeerEmbeddings(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("ungranted key → %d, want 403: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), PeerCapImages) {
		t.Errorf("refusal should name what the key does grant: %s", w.Body.String())
	}
}

// Bearer is accepted because that is what gohort's own EmbedWith sends. If this
// breaks, the consumer side stops being zero-code.
func TestPeerAcceptsBearerAsWellAsHeader(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	for _, set := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set(peerKeyHeader, pk.Key) },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+pk.Key) },
		func(r *http.Request) { r.Header.Set("Authorization", "bearer "+pk.Key) },
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
		set(r)
		if _, ok := peerFromRequest(r); !ok {
			t.Errorf("peer key not accepted via %v", r.Header)
		}
	}
}

// Refusing to relay is what keeps A→B→A from becoming an invisible hang. An
// instance that borrows its own embeddings will not serve them onward.
func TestPeerEmbeddingsRefusesToRelay(t *testing.T) {
	peerTestDB(t)
	prev := GetEmbeddingConfig()
	t.Cleanup(func() { SetEmbeddingConfig(prev) })
	SetEmbeddingConfig(EmbeddingConfig{
		Enabled:  true,
		Endpoint: "https://other-instance.example/api/peer/v1",
		Model:    "nomic-embed-text",
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings",
		strings.NewReader(`{"input":["hello"]}`))
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerEmbeddings(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a relaying instance → %d, want 503: %s", w.Code, w.Body.String())
	}
}

// Answering from a different model than the caller named returns vectors from a
// space it does not think it is in, and nothing downstream can detect that. So
// the mismatch is refused rather than served.
func TestPeerEmbeddingsRefusesAModelMismatch(t *testing.T) {
	peerTestDB(t)
	prev := GetEmbeddingConfig()
	t.Cleanup(func() { SetEmbeddingConfig(prev) })
	SetEmbeddingConfig(EmbeddingConfig{
		Enabled: true, Endpoint: "http://localhost:11434/api", Model: "nomic-embed-text",
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-3-small","input":["hello"]}`))
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerEmbeddings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("model mismatch → %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nomic-embed-text") {
		t.Errorf("the refusal should name the model actually in use: %s", w.Body.String())
	}
}

// Both OpenAI input shapes are accepted: a bare string and an array.
func TestPeerEmbedRequestAcceptsBothInputShapes(t *testing.T) {
	var one peerEmbedRequest
	json.Unmarshal([]byte(`{"input":"hello"}`), &one)
	if got := one.inputs(); len(got) != 1 || got[0] != "hello" {
		t.Errorf("bare string input = %q", got)
	}
	var many peerEmbedRequest
	json.Unmarshal([]byte(`{"input":["a","b"]}`), &many)
	if got := many.inputs(); len(got) != 2 || got[1] != "b" {
		t.Errorf("array input = %q", got)
	}
	var none peerEmbedRequest
	json.Unmarshal([]byte(`{}`), &none)
	if got := none.inputs(); len(got) != 0 {
		t.Errorf("absent input = %q, want none", got)
	}
}

// The rate ceiling exists so a runaway peer cannot saturate this instance.
func TestPeerRateLimitStopsARunawayPeer(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 3)
	for i := 0; i < 3; i++ {
		if !peerRateAllow(pk) {
			t.Fatalf("call %d refused under a ceiling of 3", i+1)
		}
	}
	if peerRateAllow(pk) {
		t.Error("the 4th call was allowed under a ceiling of 3")
	}
}
