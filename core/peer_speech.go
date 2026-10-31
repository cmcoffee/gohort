// Speech sharing: text in, spoken audio out, on this instance's speech server
// for a peer.
//
// Wire shape is the OpenAI /audio/speech one, JSON in and audio out, plus the
// GET /audio/voices list Kokoro-FastAPI serves, for the reason transcription
// speaks OpenAI: core/media's SpeakWith and SpeechVoices already call exactly
// those under {endpoint}. Mounted at /api/peer/v1, a consuming instance points
// its ordinary SpeechConfig at the peer, and spoken replies, read-aloud and the
// voice pickers keep working with no idea the speech server is elsewhere.
//
// A resource, not the instance: the peer sends the words and gets the sound
// back. Nothing of this instance's is read for it, and the voice is the
// caller's choice from the speech server's own list.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/media"
)

// peerSpeechBudget bounds one clip. Speech on a CPU-only server takes a while
// for a long reply, but finite, so a wedged backend cannot pin a peer's
// connection open. Matches SpeakWith's own ceiling.
const peerSpeechBudget = 2 * time.Minute

// maxPeerSpeechBytes caps one request body. The words of the longest reply
// anything here reads aloud are a few kilobytes; the cap is generous and
// still keeps a request from spending this instance's memory.
const maxPeerSpeechBytes = 64 << 10

// peerSpeechOffer tells a peer what it will be speaking with. Model is
// advisory, as it is for transcription: another speech model sounds
// different, it does not break anything.
type peerSpeechOffer struct {
	Model string `json:"model,omitempty"`
	Path  string `json:"path"` // where to point an OpenAI-shaped client
}

// peerSpeechInfo describes this instance's speech for the manifest, or nil
// when it has none: a peer must not be pointed at an endpoint that answers
// "speech disabled" to everything.
func peerSpeechInfo(baseURL string) *peerSpeechOffer {
	cfg := media.GetSpeechConfig()
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		return nil
	}
	return &peerSpeechOffer{Model: strings.TrimSpace(cfg.Model), Path: baseURL}
}

// peerSpeechConfig is this instance's speech config when it may be lent, or
// writes the refusal and reports false. A config that borrows from a peer
// itself is not relayed: A to B to A is a loop neither side can see, and it
// fails as a hang.
func peerSpeechConfig(w http.ResponseWriter) (media.SpeechConfig, bool) {
	cfg := media.GetSpeechConfig()
	if strings.Contains(cfg.Endpoint, "/api/peer/") {
		peerDeny(w, http.StatusServiceUnavailable,
			"this instance borrows its own speech from another peer and will not relay it")
		return cfg, false
	}
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		peerDeny(w, http.StatusServiceUnavailable, "this instance has speech disabled")
		return cfg, false
	}
	return cfg, true
}

// handlePeerSpeech serves POST /api/peer/v1/audio/speech.
func handlePeerSpeech(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapSpeech)
	if !ok {
		return
	}
	r, release, ok := peerHold(w, r, k, PeerCapSpeech)
	if !ok {
		return
	}
	defer release()
	cfg, ok := peerSpeechConfig(w)
	if !ok {
		return
	}
	var req struct {
		Input string `json:"input"`
		Voice string `json:"voice"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPeerSpeechBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		peerDeny(w, http.StatusBadRequest,
			"expected a JSON body with \"input\" (OpenAI /audio/speech shape): "+err.Error())
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		peerDeny(w, http.StatusBadRequest, "\"input\" is required: it is the text to speak")
		return
	}
	// The voice is the caller's to choose, from this server's list; none means
	// this instance's own. The model is this instance's: it is what is lent.
	if v := strings.TrimSpace(req.Voice); v != "" {
		cfg.Voice = v
	}
	ctx, cancel := context.WithTimeout(r.Context(), peerSpeechBudget)
	defer cancel()
	audio, ext, err := media.SpeakWith(ctx, cfg, req.Input)
	if err != nil {
		peerDeny(w, http.StatusBadGateway, "speech failed: "+err.Error())
		return
	}
	Debug("[peer] %q spoke %d chars as %d bytes", k.Label, len(req.Input), len(audio))
	w.Header().Set("Content-Type", media.SpeechMIME(ext))
	w.Write(audio)
	touchPeerKey(k)
}

// handlePeerSpeechVoices serves GET /api/peer/v1/audio/voices: the voices this
// instance's speech server offers, in Kokoro-FastAPI's {"voices": [...]} shape,
// so a peer's voice pickers list them. Empty for a server with no list.
func handlePeerSpeechVoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		peerDeny(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapSpeech)
	if !ok {
		return
	}
	cfg, ok := peerSpeechConfig(w)
	if !ok {
		return
	}
	voices := media.SpeechVoices(r.Context(), cfg)
	if voices == nil {
		voices = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"voices": voices})
	touchPeerKey(k)
}

// --- consuming half ----------------------------------------------------------

// SpeechURL is where a peer's OpenAI-shaped speech client should point: the
// same /api/peer/v1 base the other OpenAI-shaped capabilities hang off.
func (p RemotePeer) SpeechURL() string {
	return strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1"
}

// ResolveSpeechProvider validates a submitted speech config and fills in the
// fields to store. When provider names a peer, the endpoint, model and
// credential come from that peer's record, so the stored config is an
// ordinary one SpeakWith uses with no peer awareness. The voice stays the
// operator's. Mirrors ResolveTranscribeProvider, down to refusing an unknown
// peer rather than quietly speaking locally, and to a blank provider keeping
// a peer selection (blank means the picker was not rendered).
func ResolveSpeechProvider(cfg media.SpeechConfig, provider string) (media.SpeechConfig, error) {
	provider = strings.TrimSpace(provider)
	// A service connector (core/service) is used as it is named; the admin
	// handler checks that it provides this.
	if strings.HasPrefix(provider, media.ServiceProviderPrefix) {
		cfg.Provider = provider
		return cfg, nil
	}
	if provider == "" || provider == EmbeddingProviderLocal {
		return cfg, nil
	}
	p, ok := PeerFromProvider(provider)
	if !ok {
		return cfg, fmt.Errorf("no peer named %q is registered: add it under Peers first",
			strings.TrimPrefix(provider, peerProviderPrefix))
	}
	if !p.Offers(PeerCapSpeech) {
		return cfg, fmt.Errorf("peer %q does not offer speech (it offers: %s)",
			p.Name, strings.Join(p.Caps, ", "))
	}
	cfg.Endpoint = p.SpeechURL()
	cfg.Model = p.SpeechModel
	cfg.APIKey = PeerCredential(p)
	cfg.Provider = provider
	cfg.Enabled = true
	return cfg, nil
}

func init() { media.SpeechResolver = resolveSpeechPeer }

// resolveSpeechPeer overlays the CURRENT peer record onto a stored speech
// config whose Provider names a peer, so a rotated credential takes effect
// without anyone saving the form again. A peer that has gone or stopped
// offering speech keeps the last endpoint that worked, and logs once, as
// resolveTranscribePeer does.
func resolveSpeechPeer(cfg media.SpeechConfig) media.SpeechConfig {
	provider := strings.TrimSpace(cfg.Provider)
	if !strings.HasPrefix(provider, peerProviderPrefix) {
		return cfg
	}
	name := strings.TrimPrefix(provider, peerProviderPrefix)
	p, ok := lookupPeerCached(name)
	if !ok {
		warnPeerResolveOnce("speech:"+name, fmt.Sprintf(
			"speech is configured against peer %q, which is no longer registered: "+
				"still using its last known endpoint %s", name, cfg.Endpoint))
		return cfg
	}
	if !p.Offers(PeerCapSpeech) {
		warnPeerResolveOnce("speech:"+name, fmt.Sprintf(
			"peer %q no longer offers speech (it offers: %s): "+
				"still using its last known endpoint %s", name, strings.Join(p.Caps, ", "), cfg.Endpoint))
		return cfg
	}
	warnPeerResolveOnce("speech:"+name, "")
	cfg.Endpoint = p.SpeechURL()
	cfg.APIKey = PeerCredential(p)
	if strings.TrimSpace(p.SpeechModel) != "" {
		cfg.Model = p.SpeechModel
	}
	return cfg
}
