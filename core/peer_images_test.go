package core

import (
	"encoding/json"
	"fmt"
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

// Source photos aimed at a text-only renderer mean the CALLER's copy of what
// this instance offers has gone stale. Left to fall through, the edit path
// refuses deep inside and the peer gets a 502 — which reads as "the machine I
// borrowed is broken" and points the operator at the wrong end of the link. It
// has to come back as a bad request that names the repair.
func TestImageRenderRefusesSourcePhotosOnATextOnlyBackend(t *testing.T) {
	peerImageDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapImages}, 0)

	spec, _ := json.Marshal(RestImageSpec{
		SubmitURL: "https://gpu.example/render", SubmitMethod: "POST",
		SubmitBody: `{"prompt":"{prompt}"}`, ImageB64Path: "images.0", Credential: "no_auth",
	})
	if err := SaveConnector(RootDB, Connector{Name: "comfyui", Kind: RestImageConnectorKind, Spec: spec}); err != nil {
		t.Fatalf("saving the text-only backend: %v", err)
	}
	if err := ApproveConnector(RootDB, "comfyui"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/images/render",
		strings.NewReader(`{"prompt":"make it sunny","backend":"comfyui","init_images":["aGVsbG8="]}`))
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerImageRender(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("editing on a text-only backend → %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "refresh") {
		t.Errorf("the refusal should tell the caller how to repair its stale record: %s", w.Body.String())
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

// An edit shipped by a peer must actually reach the backend.
//
// peerRenderEdit decodes the source photos to disk and then hands their paths
// to EditImageWithBackend through a synthetic session — but the session had no
// workspace root, and resolveInputImages resolves every ref against
// sess.WorkspaceDir and rejects absolute paths outright. So every peer edit
// died at the last branch of ref resolution with "no workspace available to
// read /opt/gohort/data/images/….png from": an error quoting a serving-side
// absolute path, returned over HTTP to a different machine, about a directory
// that exists and a file that had just been written into it.
func TestPeerEditReachesTheBackend(t *testing.T) {
	peerImageDB(t)
	prevDir := ImageDir()
	SetImageDir(t.TempDir())
	t.Cleanup(func() { SetImageDir(prevDir) })

	// A real editing backend: {images} in the body is what makes it one. It
	// answers with a 1x1 PNG so the whole path runs.
	var gotSources int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InitImages []string `json:"init_images"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotSources = len(body.InitImages)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[%q]}`, tinyPNG)
	}))
	t.Cleanup(backend.Close)

	spec, _ := json.Marshal(RestImageSpec{
		// With a path: the auto-provisioned local credential allows everything
		// UNDER its base, and a bare scheme://host:port is not under itself.
		SubmitURL: backend.URL + "/sdapi/v1/img2img", SubmitMethod: "POST",
		SubmitBody:   `{"prompt":"{prompt}","init_images":{images}}`,
		ImageB64Path: "images.0", Credential: "no_auth", MaxInputImages: 1,
	})
	if err := SaveConnector(RootDB, Connector{Name: "editor", Kind: RestImageConnectorKind, Spec: spec}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ApproveConnector(RootDB, "editor"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	pk, _ := MintPeerKey("mac", []string{PeerCapImages}, 0)
	body := fmt.Sprintf(`{"prompt":"put the number on one pillar","backend":"editor","init_images":[%q]}`, tinyPNG)
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/images/render", strings.NewReader(body))
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerImageRender(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("peer edit → %d, want 200: %s", w.Code, w.Body.String())
	}
	if gotSources != 1 {
		t.Errorf("the backend received %d source image(s), want 1 — the edit never carried the photo", gotSources)
	}
	var out struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Images) != 1 {
		t.Errorf("the render did not come back inline: %s", w.Body.String())
	}
}
