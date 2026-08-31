// Transcription sharing, serving half: turning speech into text on this
// instance's hardware for a peer.
//
// Wire shape is the OpenAI /audio/transcriptions multipart one — file + model +
// response_format in, plain text out — for the same reason the embeddings
// endpoint speaks OpenAI and the render endpoint speaks A1111: core/media's
// Transcribe already POSTs exactly that to {endpoint}/audio/transcriptions with
// a Bearer token. Mounted at /api/peer/v1, a consuming instance points its
// ordinary TranscribeConfig at the peer and every caller — the transcribe tool,
// the video pipeline, inbound voice notes — keeps working with no idea the
// whisper model is on another machine. A gohort-shaped protocol would have
// meant writing that side again and keeping the two in step forever.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/media"
)

// peerTranscribeBudget bounds one transcription. Whisper on a busy GPU is
// minutes for a long recording, but finite, so a wedged backend cannot pin a
// peer's connection open indefinitely. Matches the local tool's own ceiling.
const peerTranscribeBudget = 5 * time.Minute

// maxPeerAudioBytes caps one inbound clip. Audio is the heaviest REQUEST any
// peer capability carries — an hour of speech is tens of megabytes — and an
// unbounded one is a way to spend this instance's memory from off-machine.
const maxPeerAudioBytes = 64 << 20

// PeerTranscribeInfo tells a peer what it will be transcribing with.
//
// Model is advisory, unlike the embeddings model. Two embedders produce vectors
// in incomparable spaces, so a mismatch there is a silent correctness failure
// worth refusing over; two speech models produce text, and the worse one is
// merely worse. A peer that cares which it gets can read this and decide.
type PeerTranscribeInfo struct {
	Model string `json:"model,omitempty"`
	Path  string `json:"path"` // where to point an OpenAI-shaped client
}

// peerTranscribeInfo describes this instance's STT for the manifest, or nil
// when it has none configured — a peer must not be told to point at an endpoint
// that will answer "transcription disabled" to everything.
func peerTranscribeInfo(baseURL string) *PeerTranscribeInfo {
	cfg := GetTranscribeConfig()
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		return nil
	}
	return &PeerTranscribeInfo{Model: strings.TrimSpace(cfg.Model), Path: baseURL}
}

// HandlePeerTranscribe serves POST /api/peer/v1/audio/transcriptions.
func HandlePeerTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapTranscribe)
	if !ok {
		return
	}
	// Refuse to relay, exactly as embeddings does. If this instance is itself
	// borrowing transcription from a peer, serving it onward makes A→B→A a loop
	// neither side can see, and the failure is a hang rather than an error.
	cfg := GetTranscribeConfig()
	if strings.Contains(cfg.Endpoint, "/api/peer/") {
		peerDeny(w, http.StatusServiceUnavailable,
			"this instance borrows its own transcription from another peer and will not relay it")
		return
	}
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		peerDeny(w, http.StatusServiceUnavailable, "this instance has transcription disabled")
		return
	}

	// ParseMultipartForm buffers up to the given size in memory and spills the
	// rest to disk; MaxBytesReader is what actually refuses an oversized clip,
	// and it has to wrap the body BEFORE parsing or the cap arrives too late.
	r.Body = http.MaxBytesReader(w, r.Body, maxPeerAudioBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		peerDeny(w, http.StatusBadRequest,
			"expected a multipart form with a \"file\" part (OpenAI /audio/transcriptions shape): "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		peerDeny(w, http.StatusBadRequest, "the \"file\" part is required — it carries the audio to transcribe")
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(file)
	if err != nil {
		peerDeny(w, http.StatusBadRequest, "could not read the uploaded audio: "+err.Error())
		return
	}
	if len(audio) == 0 {
		peerDeny(w, http.StatusBadRequest, "the uploaded audio is empty")
		return
	}
	name := "audio.mp3"
	if header != nil && strings.TrimSpace(header.Filename) != "" {
		// Base name only. The filename reaches the STT endpoint, which uses the
		// extension to pick a decoder, and a path from off-machine has no
		// business being carried any further than that.
		name = pathBase(header.Filename)
	}

	ctx, cancel := context.WithTimeout(r.Context(), peerTranscribeBudget)
	defer cancel()
	text, err := Transcribe(ctx, audio, name)
	if err != nil {
		peerDeny(w, http.StatusBadGateway, "transcription failed: "+err.Error())
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		// Not an error. A clip with no speech in it transcribes to nothing, and
		// the caller has to be able to tell that from a fault — an empty 200
		// says "I heard nothing", a 502 says "I broke".
		Debug("[peer] %q transcribed %d bytes to no speech", k.Label, len(audio))
	} else {
		Debug("[peer] %q transcribed %d bytes to %d chars", k.Label, len(audio), len(text))
	}

	// response_format is honoured for "text" (what every gohort client asks
	// for) and defaults to the OpenAI JSON envelope otherwise, so a non-gohort
	// client pointed here still gets the shape it expects.
	if strings.EqualFold(strings.TrimSpace(r.FormValue("response_format")), "text") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, text)
		touchPeerKey(k)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"text": text})
	touchPeerKey(k)
}

// pathBase strips any directory component from an uploaded filename, on either
// separator — the sender's OS is not this one's.
func pathBase(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "audio.mp3"
	}
	return name
}

// --- consuming half ----------------------------------------------------------

// TranscribeURL is where a peer's OpenAI-shaped STT client should point.
// Mirrors EmbeddingsURL: the same /api/peer/v1 base, because both endpoints
// hang off it at their standard OpenAI paths.
func (p RemotePeer) TranscribeURL() string {
	return strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1"
}

// ResolveTranscribeProvider validates a submitted transcription config and
// fills in the fields to store. When Provider names a peer, the endpoint, model
// and key come from that peer's record — so the stored config is an ordinary
// one that Transcribe can use with no peer awareness at all.
//
// The fields it writes are a LAST-KNOWN CACHE, not the operative values:
// resolveTranscribePeer overlays the current peer record on every read (see
// GetTranscribeConfig), so a rotated peer key takes effect without anyone
// editing this form. They are still written because a peer that is later
// deleted leaves nothing to resolve, and the last endpoint that worked is a
// better answer than a blank one.
//
// Mirrors ResolveEmbeddingProvider down to the failure modes, including
// refusing an unknown peer rather than falling back to local: a silent fall
// back would send audio somewhere the operator did not choose.
func ResolveTranscribeProvider(cfg TranscribeConfig, provider string) (TranscribeConfig, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		// Same rule as ResolveEmbeddingProvider, and the same trap: the "local"
		// option submits the string "local", so blank means the dropdown was
		// not rendered — which happens whenever no peer currently offers
		// transcription. Reading that as "go local" would reset the selection
		// while leaving the peer's endpoint and credential in place, and the
		// resulting config points at the peer with a credential nothing
		// refreshes.
		if strings.HasPrefix(strings.TrimSpace(cfg.Provider), peerProviderPrefix) {
			return cfg, nil
		}
	}
	if provider == "" || provider == EmbeddingProviderLocal {
		return cfg, nil
	}
	p, ok := PeerFromProvider(provider)
	if !ok {
		return cfg, fmt.Errorf("no peer named %q is registered — add it under Peers first",
			strings.TrimPrefix(provider, peerProviderPrefix))
	}
	if !p.Offers(PeerCapTranscribe) {
		return cfg, fmt.Errorf("peer %q does not offer transcription (it offers: %s)",
			p.Name, strings.Join(p.Caps, ", "))
	}
	cfg.Endpoint = p.TranscribeURL()
	cfg.Model = p.TranscribeModel
	// PeerCredential, not the raw key. The key authenticates nothing since
	// exchange became mandatory (peer_token.go) — it is a pairing code. This
	// resolver is the CONFIG-time twin of the read-time overlay below, and it
	// snapshotting the key was a latent bug the whole time: it produced a
	// config that worked only while the static key was still accepted.
	cfg.APIKey = PeerCredential(p)
	cfg.Enabled = true
	return cfg, nil
}

func init() {
	media.TranscribeResolver = resolveTranscribePeer
	// And the transport the request rides out on. The resolver above keeps the
	// endpoint current; this is what keeps the CREDENTIAL current, which the
	// resolver cannot do — it runs under GetTranscribeConfig, on the per-upload
	// enabled-check path, where blocking for a token exchange is not allowed.
	media.HTTPClientForProvider = PeerClientForProvider
}

// resolveTranscribePeer overlays the CURRENT peer record onto a stored
// transcription config whose Provider names a peer.
//
// The live half of peer transcription, and the reason it exists is the bug the
// embeddings side already had: save-time resolution copied the peer's endpoint
// and key into the stored config, so rotating that peer's key left a
// configuration that looked correct and answered 401, with nothing on either
// screen saying which of the two records had gone stale. Reading the peer here
// makes the stored config a POINTER and leaves one place the key lives — which
// is also the precondition for peer credentials that rotate on their own.
//
// A peer that has gone missing or stopped offering transcription keeps the
// stored fields — the last endpoint that worked — and logs once. Blanking them
// would turn every voice note into a silent skip, which reads as "transcription
// is off" rather than "this peer is gone".
func resolveTranscribePeer(cfg TranscribeConfig) TranscribeConfig {
	provider := strings.TrimSpace(cfg.Provider)
	if !strings.HasPrefix(provider, peerProviderPrefix) {
		return cfg
	}
	name := strings.TrimPrefix(provider, peerProviderPrefix)
	p, ok := lookupPeerCached(name)
	if !ok {
		warnPeerResolveOnce("transcribe:"+name, fmt.Sprintf(
			"transcription is configured against peer %q, which is no longer registered — "+
				"still using its last known endpoint %s", name, cfg.Endpoint))
		return cfg
	}
	if !p.Offers(PeerCapTranscribe) {
		warnPeerResolveOnce("transcribe:"+name, fmt.Sprintf(
			"peer %q no longer offers transcription (it offers: %s) — "+
				"still using its last known endpoint %s", name, strings.Join(p.Caps, ", "), cfg.Endpoint))
		return cfg
	}
	warnPeerResolveOnce("transcribe:"+name, "") // clears the warning once the peer is healthy again
	cfg.Endpoint = p.TranscribeURL()
	cfg.APIKey = PeerCredential(p)
	// The MODEL is only overridden when the peer reports one, matching
	// resolveEmbeddingPeer: a single-model whisper backend advertises none, and
	// overwriting a deliberately-set value with "" would send a request with no
	// model to a server that wants one.
	if strings.TrimSpace(p.TranscribeModel) != "" {
		cfg.Model = p.TranscribeModel
	}
	return cfg
}
