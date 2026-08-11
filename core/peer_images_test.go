package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func peerImageDB(t *testing.T) {
	t.Helper()
	prevRoot, prevAuth := RootDB, AuthDB
	// Secure() caches its store on first use, so the instance is reset too —
	// otherwise a store from an earlier test leaks into this one.
	prevSecure := secureAPIInstance
	t.Cleanup(func() {
		RootDB, AuthDB = prevRoot, prevAuth
		secureAPIInstanceMu.Lock()
		secureAPIInstance = prevSecure
		secureAPIInstanceMu.Unlock()
	})
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	secureAPIInstanceMu.Lock()
	secureAPIInstance = nil
	secureAPIInstanceMu.Unlock()
}

// A key granted embeddings must not be able to spend the GPU. The capability
// allowlist is the whole point of granting one thing rather than everything.
func TestImageRenderRefusesAKeyWithoutTheGrant(t *testing.T) {
	peerImageDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/images/render",
		strings.NewReader(`{"prompt":"a duck"}`))
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerImageRender(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("embeddings-only key rendering → %d, want 403: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), PeerCapEmbeddings) {
		t.Errorf("the refusal should name what the key does grant: %s", w.Body.String())
	}
}

// Naming a backend this instance does not offer must be refused. Without the
// check a peer could render through any connector by string — including one the
// operator never meant to share, and which the manifest never advertised.
func TestImageRenderRefusesAnUnofferedBackend(t *testing.T) {
	peerImageDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapImages}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/images/render",
		strings.NewReader(`{"prompt":"a duck","backend":"someone-elses-private-graph"}`))
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerImageRender(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unoffered backend → %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "someone-elses-private-graph") {
		t.Errorf("the refusal should name the backend asked for: %s", w.Body.String())
	}
}

// A request with neither a prompt nor a source image has nothing to render.
func TestImageRenderNeedsSomethingToRender(t *testing.T) {
	peerImageDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapImages}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/images/render", strings.NewReader(`{}`))
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerImageRender(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty render request → %d, want 400", w.Code)
	}
}

// The manifest advertises renderers only to a key granted images, and the
// images key is absent entirely otherwise — a peer must not learn what hardware
// is on offer from a grant it does not hold.
func TestManifestWithholdsRenderersFromAnUngrantedKey(t *testing.T) {
	peerImageDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerManifest(w, r)

	var m PeerManifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Images != nil {
		t.Errorf("renderers advertised to a key without the images grant: %+v", m.Images)
	}
	// And images is reported as served-but-not-granted, so the peer can tell
	// this from a build that cannot render at all.
	for _, e := range m.Capabilities {
		if e.Name != PeerCapImages {
			continue
		}
		if !e.Served {
			t.Error("images is implemented but reported unserved")
		}
		if e.Granted {
			t.Error("images reported granted to a key that was not given it")
		}
		if !strings.Contains(e.Note, "not granted") {
			t.Errorf("the note should distinguish ungranted from unbuilt: %q", e.Note)
		}
	}
}

// Images is served now. The manifest must say so, or a peer will not offer it
// as a choice — and the admin copy is derived from the same predicate, so this
// pins both.
func TestImagesIsReportedAsServed(t *testing.T) {
	if !PeerCapabilityServed(PeerCapImages) {
		t.Error("images should be served by this build")
	}
	if PeerCapabilityServed(PeerCapModels) || PeerCapabilityServed(PeerCapTranscode) {
		t.Error("models/transcode are not implemented and must not report as served")
	}
}
