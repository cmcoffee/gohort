package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// THE PROPERTY: serving a peer leaves NO trace in this instance's own state.
//
// Embeddings, images, transcription, search, browse and models are ANONYMOUS
// COMPUTE — an input goes in, a derived output comes back, and nothing about
// the request belongs to anybody here. Investigate, knowledge and exec are the
// deliberate exception: they name an Owner because they reach that user's
// systems, and they write to that user's stores on purpose.
//
// The anonymous half is currently clean by construction rather than by
// enforcement, which is the fragile kind: adding a vector cache to Embed, or
// saving a render into a gallery, would mix a peer's work into a local user's
// data and nothing would notice. These tests are the enforcement.

// isolationRig gives a test its own stores and image directory, and returns the
// image dir so a test can assert about what landed in it.
func isolationRig(t *testing.T) (key PeerKey, imgDir string) {
	t.Helper()
	prevRoot, prevAuth, prevVec := RootDB, AuthDB, VectorDB
	prevSecure := secureAPIInstance
	prevImg := imageDir
	t.Cleanup(func() {
		RootDB, AuthDB, VectorDB = prevRoot, prevAuth, prevVec
		imageDir = prevImg
		secureAPIInstanceMu.Lock()
		secureAPIInstance = prevSecure
		secureAPIInstanceMu.Unlock()
	})
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	VectorDB = &DBase{Store: kvlite.MemStore()}
	secureAPIInstanceMu.Lock()
	secureAPIInstance = nil
	secureAPIInstanceMu.Unlock()

	imgDir = t.TempDir()
	SetImageDir(imgDir)

	pk, err := MintPeerKey("mac", PeerCapabilities(), 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return pk, imgDir
}

// userVisibleImages counts files under the per-user image areas — kept,
// recent and delivered. These are the directories a person's gallery reads, and
// a peer's picture appearing in one is the mixing this all guards against.
func userVisibleImages(t *testing.T, imgDir string) []string {
	t.Helper()
	var found []string
	for _, area := range []string{"kept", "recent", "delivered"} {
		root := filepath.Join(imgDir, area)
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			found = append(found, p)
			return nil
		})
	}
	return found
}

// TestServingEmbeddingsStoresNothing — the highest-volume capability. A peer
// grinding a corpus through this instance's embedder must not deposit any of
// that corpus here.
func TestServingEmbeddingsStoresNothing(t *testing.T) {
	pk, _ := isolationRig(t)
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: "http://127.0.0.1:1/v1", Model: "m"})

	before := len(VectorDB.Keys(EmbeddedChunks))
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings",
		strings.NewReader(`{"input":["a private sentence from the peer's corpus"]}`))
	r.Header.Set(peerKeyHeader, pk.Key)
	HandlePeerEmbeddings(httptest.NewRecorder(), r)
	// The embedder is unreachable, so this 502s — which is the point: even the
	// FAILURE path must not have written the caller's text anywhere.
	if after := len(VectorDB.Keys(EmbeddedChunks)); after != before {
		t.Errorf("serving a peer embedding wrote %d chunk(s) into this instance's vector store — "+
			"the peer's text is now searchable by local agents", after-before)
	}
}

// TestServingAnImageLeavesNothingInAnyGallery — a peer's source photos and its
// render pass through this machine's disk. None of it may end up anywhere a
// person's gallery looks.
func TestServingAnImageLeavesNothingInAnyGallery(t *testing.T) {
	pk, imgDir := isolationRig(t)

	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nnot really a png"))
	body, _ := json.Marshal(map[string]any{
		"prompt": "a face", "init_images": []string{png}, "backend": "nonexistent",
	})
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/images/render", strings.NewReader(string(body)))
	r.Header.Set(peerKeyHeader, pk.Key)
	HandlePeerImageRender(httptest.NewRecorder(), r)

	if got := userVisibleImages(t, imgDir); len(got) > 0 {
		t.Errorf("serving a peer render left %d file(s) in a per-user image area: %v", len(got), got)
	}
	// And nothing lingers loose in the working root either — a refused or failed
	// render must clean up after itself as thoroughly as a successful one.
	entries, _ := os.ReadDir(imgDir)
	var loose []string
	for _, e := range entries {
		if !e.IsDir() {
			loose = append(loose, e.Name())
		}
	}
	if len(loose) > 0 {
		t.Errorf("a peer's image bytes are still on disk after the request: %v", loose)
	}
}

// TestServingATranscriptionKeepsNoAudio — multipart spills to disk past its
// memory budget, so the cleanup is what stops a peer's recording staying here.
func TestServingATranscriptionKeepsNoAudio(t *testing.T) {
	pk, _ := isolationRig(t)
	SetTranscribeConfig(TranscribeConfig{Enabled: true, Endpoint: "http://127.0.0.1:1/v1", Model: "whisper-1"})

	var buf strings.Builder
	const boundary = "xxBOUNDARYxx"
	fmt.Fprintf(&buf, "--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n\r\n", boundary)
	buf.WriteString(strings.Repeat("audio-bytes", 2000))
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)

	before := tempFileCount(t)
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/audio/transcriptions", strings.NewReader(buf.String()))
	r.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	r.Header.Set(peerKeyHeader, pk.Key)
	HandlePeerTranscribe(httptest.NewRecorder(), r)

	if after := tempFileCount(t); after > before {
		t.Errorf("serving a transcription left %d spilled multipart file(s) behind", after-before)
	}
}

func tempFileCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "multipart-") {
			n++
		}
	}
	return n
}

// TestOnlyTheScopedCapabilitiesNameAnOwner — the line between anonymous compute
// and work done on a person's behalf.
//
// Investigate, knowledge and exec write to a user's stores BY DESIGN, which is
// exactly why they require an Owner and an explicit appliance list. The others
// must never be able to name a user at all: a capability that could would be
// one edit away from filing a peer's work under somebody here.
func TestOnlyTheScopedCapabilitiesNameAnOwner(t *testing.T) {
	scoped := map[string]bool{
		PeerCapInvestigate: true,
		PeerCapKnowledge:   true,
		PeerCapExec:        true,
	}
	k := PeerKey{Caps: PeerCapabilities(), Owner: "craig", Appliances: []string{"box-1"}}
	for _, c := range PeerCapabilities() {
		got := k.AllowsApplianceFor(c, "box-1")
		if got != scoped[c] {
			t.Errorf("capability %q: appliance-scoped=%v, want %v — the anonymous capabilities "+
				"must not be able to reach a named user's systems", c, got, scoped[c])
		}
	}
	// And a scoped capability with no owner reaches nothing, so a key that was
	// granted one without being told whose systems is inert rather than broad.
	noOwner := PeerKey{Caps: []string{PeerCapExec}, Appliances: []string{"box-1"}}
	if noOwner.AllowsApplianceFor(PeerCapExec, "box-1") {
		t.Error("an exec grant with no Owner reached an appliance")
	}
}

// TestAPeerKeyNeverResolvesToAUser — the central design guarantee, asserted
// rather than assumed. If a peer key ever became a username, every isolation
// property above would be irrelevant: the bearer would simply BE somebody here.
func TestAPeerKeyNeverResolvesToAUser(t *testing.T) {
	src, err := os.ReadFile("peer_serve.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, forbidden := range []string{"AuthCurrentUser(", "userFromAPIKey(", "RegisterAPIKeyValidator("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("a peer handler calls %s — a peer key would become a user, and every "+
				"capability would silently widen to everything that user owns", forbidden)
		}
	}
}
