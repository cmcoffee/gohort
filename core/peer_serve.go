// The serving half of resource sharing: the HTTP surface a peer instance calls.
//
// Shape note. The embeddings endpoint speaks the OpenAI /embeddings wire format
// rather than something gohort-specific, and that is the entire consumer-side
// implementation. EmbedWith already POSTs {model, input:[...]} to
// <endpoint>/embeddings with a bearer token and already parses the OpenAI
// response shape, so a peer points its existing embedding config at
// https://host/api/peer/v1 with the key as the API key and is done. No client,
// no connector, no new code path to keep in step with the local one.
//
// Handlers here NEVER consult AuthCurrentUser or userFromAPIKey. A peer key is
// not a user (see peer_key.go); authentication is peerFromRequest and
// authorization is PeerKey.Allows, and nothing else is in scope.
package core

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// peerKeyHeader is the dedicated header. Authorization: Bearer also works,
// because that is what an OpenAI-shaped client (including gohort's own
// EmbedWith) sends without being taught anything new.
const peerKeyHeader = "X-Gohort-Peer-Key"

// peerFromRequest authenticates a peer request. Returns false for anything
// unrecognized, disabled, or absent.
func peerFromRequest(r *http.Request) (PeerKey, bool) {
	secret := strings.TrimSpace(r.Header.Get(peerKeyHeader))
	if secret == "" {
		if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
			if lower := strings.ToLower(auth); strings.HasPrefix(lower, "bearer ") {
				secret = strings.TrimSpace(auth[len("bearer "):])
			}
		}
	}
	if secret == "" {
		return PeerKey{}, false
	}
	return LookupPeerKey(secret)
}

// peerDeny writes a JSON error. Peers are machines: the body says what is wrong
// in a form a log will keep, and the status distinguishes "who are you" from
// "not for you" from "slow down".
func peerDeny(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// peerAuthorize runs the checks every peer handler needs: authenticate, confirm
// the capability is granted, and charge the rate limit. Returns false having
// already written the response.
func peerAuthorize(w http.ResponseWriter, r *http.Request, capability string) (PeerKey, bool) {
	// Before the lookup, which reads every issued key: a source that keeps
	// failing must not keep buying that work. See peerSourceThrottled.
	if peerSourceThrottled(r) {
		w.Header().Set("Retry-After", "60")
		peerDeny(w, http.StatusTooManyRequests, "too many failed authentication attempts from this address")
		return PeerKey{}, false
	}
	k, ok := peerFromRequest(r)
	if !ok {
		peerNoteAuthFailure(r)
		peerDeny(w, http.StatusUnauthorized, "unrecognized or disabled peer key")
		return PeerKey{}, false
	}
	if !k.Allows(capability) {
		// Name what the key DOES grant. A peer misconfigured against the wrong
		// key otherwise cannot tell that from a capability this instance does
		// not offer at all.
		peerDeny(w, http.StatusForbidden, "this key does not grant "+capability+
			" (granted: "+strings.Join(k.Caps, ", ")+")")
		return PeerKey{}, false
	}
	if !peerRateAllow(k) {
		w.Header().Set("Retry-After", "60")
		peerDeny(w, http.StatusTooManyRequests, "rate limit reached for this key")
		return PeerKey{}, false
	}
	return k, true
}

// --- manifest ----------------------------------------------------------------

// PeerManifest is what this instance advertises to a peer. Every capability
// name appears, with served=false for the ones this build cannot do and
// granted=false for the ones the calling key was not given — a peer debugging
// its config should learn which of the two it is hitting without guessing.
type PeerManifest struct {
	Instance     string              `json:"instance"`
	Capabilities []PeerManifestEntry `json:"capabilities"`
	Embeddings   *PeerEmbeddingsInfo `json:"embeddings,omitempty"`
	// Images lists the renderers on offer. The consuming side turns each into
	// a local backend entry, so this carries what that entry needs to be
	// truthful — whether it edits, and how many source photos it takes.
	Images []PeerImageBackend `json:"images,omitempty"`
	// Investigable lists the appliances the CALLING key may ask about. Scoped
	// to the key rather than to the instance: two peers holding different keys
	// see different lists, and a key granted the capability but no appliances
	// sees an empty one — which is the honest rendering of "granted nothing".
	Investigable []PeerInvestigable `json:"investigable,omitempty"`
	// Transcribe says where to point an OpenAI-shaped STT client, and with
	// which model. Absent when this instance has no transcription configured —
	// advertising a path that answers "disabled" to everything is worse than
	// saying nothing.
	Transcribe *PeerTranscribeInfo `json:"transcribe,omitempty"`
	// Search says where to point a SearXNG-shaped client and which upstream
	// this instance actually uses — a free DuckDuckGo relay and a metered
	// Serper key are the same endpoint and very different favours.
	Search *PeerSearchInfo `json:"search,omitempty"`
	// Browse is the page-rendering endpoint, present only when a browser is
	// linked into this build.
	Browse string `json:"browse,omitempty"`
	// Models lists the local models this instance will run for the caller, one
	// per tier. Named rather than counted: the far side configures a model id,
	// and a peer offering both a fast worker and a slow lead is offering a
	// choice the operator has to be able to see in order to make.
	Models []PeerModelInfo `json:"models,omitempty"`
}

type PeerManifestEntry struct {
	Name    string `json:"name"`
	Served  bool   `json:"served"`  // this build implements it
	Granted bool   `json:"granted"` // the calling key was given it
	Note    string `json:"note,omitempty"`
}

// PeerEmbeddingsInfo tells a peer what vector space it will be writing into.
//
// The model matters more than it looks. Vectors from two different embedders
// are not comparable, and a mismatch degrades every cosine comparison silently
// rather than failing — which is exactly what EmbedVersion exists to prevent
// locally. A peer needs the same fact to configure itself consistently.
type PeerEmbeddingsInfo struct {
	Model string `json:"model,omitempty"`
	Dim   int    `json:"dim,omitempty"`
	Path  string `json:"path"` // where to point an OpenAI-shaped client
}

// HandlePeerManifest serves GET /api/peer/manifest.
func HandlePeerManifest(w http.ResponseWriter, r *http.Request) {
	// The manifest authenticates on its own rather than through peerAuthorize
	// (it is not gated on one capability), so it needs the same throttle — an
	// unthrottled endpoint here would simply be the one an attacker used.
	if peerSourceThrottled(r) {
		w.Header().Set("Retry-After", "60")
		peerDeny(w, http.StatusTooManyRequests, "too many failed authentication attempts from this address")
		return
	}
	k, ok := peerFromRequest(r)
	if !ok {
		peerNoteAuthFailure(r)
		peerDeny(w, http.StatusUnauthorized, "unrecognized or disabled peer key")
		return
	}
	if !peerRateAllow(k) {
		w.Header().Set("Retry-After", "60")
		peerDeny(w, http.StatusTooManyRequests, "rate limit reached for this key")
		return
	}
	// Hostname, not a product name: a peer with two providers configured needs
	// to know WHICH one answered.
	host, _ := os.Hostname()
	m := PeerManifest{Instance: host}
	for _, name := range PeerCapabilities() {
		e := PeerManifestEntry{Name: name, Served: peerCapServed(name), Granted: k.Allows(name)}
		switch {
		case !e.Served && name == PeerCapModels:
			// Distinguished from the generic case: this one is a configuration
			// fact the operator can act on, not a missing feature.
			e.Note = "this instance has no local model to lend — inference sharing serves llama.cpp and ollama only"
		case !e.Served:
			e.Note = "not implemented by this instance yet"
		case e.Served && !e.Granted:
			e.Note = "offered, but this key was not granted it"
		case name == PeerCapInvestigate && len(k.Appliances) == 0:
			// Granted the capability and no appliances reaches nothing. Said
			// plainly, because the capability list otherwise reads as working.
			e.Note = "granted, but this key names no appliances — it can reach none"
		case name == PeerCapInvestigate && strings.TrimSpace(k.Owner) == "":
			e.Note = "granted, but this key has no owner — an investigation runs as a user and there is none"
		}
		m.Capabilities = append(m.Capabilities, e)
	}
	if inv := peerInvestigablesFor(k); len(inv) > 0 {
		m.Investigable = inv
	}
	if k.Allows(PeerCapEmbeddings) && peerCapServed(PeerCapEmbeddings) {
		cfg := GetEmbeddingConfig()
		info := &PeerEmbeddingsInfo{Model: cfg.Model, Path: "/api/peer/v1"}
		// Report the dimension by embedding a trivial string. A peer sizing its
		// vector store should not have to discover this by storing one and
		// finding out.
		if vec, err := Embed(r.Context(), "dimension probe"); err == nil {
			info.Dim = len(vec)
		}
		m.Embeddings = info
	}
	if k.Allows(PeerCapImages) && peerCapServed(PeerCapImages) {
		m.Images = peerImageBackends()
	}
	if k.Allows(PeerCapTranscribe) && peerCapServed(PeerCapTranscribe) {
		m.Transcribe = peerTranscribeInfo("/api/peer/v1")
	}
	if k.Allows(PeerCapSearch) && peerCapServed(PeerCapSearch) {
		m.Search = peerSearchInfo("/api/peer/v1")
	}
	if k.Allows(PeerCapBrowse) && peerCapServed(PeerCapBrowse) && peerBrowseServed() {
		m.Browse = "/api/peer/v1/browse"
	}
	if k.Allows(PeerCapModels) && peerCapServed(PeerCapModels) {
		m.Models = peerModelsInfo("/api/peer/v1")
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
	touchPeerKey(k)
}

// --- embeddings --------------------------------------------------------------

// peerEmbedRequest is the OpenAI /embeddings request. input accepts a bare
// string or an array, because both are in the wild and a peer should not have
// to care which this instance prefers.
type peerEmbedRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

// inputs normalizes the two accepted shapes into a slice.
func (p peerEmbedRequest) inputs() []string {
	if len(p.Input) == 0 {
		return nil
	}
	var many []string
	if err := json.Unmarshal(p.Input, &many); err == nil {
		return many
	}
	var one string
	if err := json.Unmarshal(p.Input, &one); err == nil {
		return []string{one}
	}
	return nil
}

// maxPeerEmbedInputs caps a single batch. Unbounded, one request could tie up
// the embedder for minutes on someone else's behalf while local turns wait
// behind it.
const maxPeerEmbedInputs = 64

// HandlePeerEmbeddings serves POST /api/peer/v1/embeddings in the OpenAI wire
// format, backed by whatever embedder this instance is configured to use.
func HandlePeerEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapEmbeddings)
	if !ok {
		return
	}
	// Refuse to relay. If this instance is itself borrowing embeddings from a
	// peer, serving them onward makes A→B→A a loop that neither side can see,
	// and the failure is a hang rather than an error. Chaining could be made
	// safe with hop accounting; until it is, the honest answer is no.
	if local := GetEmbeddingConfig(); strings.Contains(local.Endpoint, "/api/peer/") {
		peerDeny(w, http.StatusServiceUnavailable,
			"this instance borrows its own embeddings from another peer and will not relay them")
		return
	}

	var req peerEmbedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		peerDeny(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	inputs := req.inputs()
	if len(inputs) == 0 {
		peerDeny(w, http.StatusBadRequest, "input is required (a string or an array of strings)")
		return
	}
	if len(inputs) > maxPeerEmbedInputs {
		peerDeny(w, http.StatusBadRequest, "too many inputs in one request")
		return
	}
	cfg := GetEmbeddingConfig()
	if !cfg.Enabled {
		peerDeny(w, http.StatusServiceUnavailable, "this instance has embeddings disabled")
		return
	}
	// A named model that is not the one in use is refused rather than quietly
	// served by a different embedder. Silently answering from another model
	// returns vectors from a space the caller does not think it is in, and
	// nothing downstream can detect that.
	if want := strings.TrimSpace(req.Model); want != "" && cfg.Model != "" && want != cfg.Model {
		peerDeny(w, http.StatusBadRequest,
			"this instance embeds with "+cfg.Model+", not "+want+
				" — vectors from two models are not comparable, so the request is refused rather than answered from the wrong space")
		return
	}

	type item struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	out := struct {
		Object string `json:"object"`
		Data   []item `json:"data"`
		Model  string `json:"model"`
	}{Object: "list", Model: cfg.Model}

	// Label the work as this peer's. The local scheduler round-robins between
	// callers, so a peer running bulk ingestion interleaves with local turns
	// instead of arriving as an anonymous flood that looks like local work and
	// delays the conversation a person is waiting on.
	ctx := WithEmbedCaller(r.Context(), "peer:"+k.Label)

	started := time.Now()
	for i, text := range inputs {
		vec, err := Embed(ctx, text)
		if err != nil {
			peerDeny(w, http.StatusBadGateway, "embed failed: "+err.Error())
			return
		}
		out.Data = append(out.Data, item{Object: "embedding", Index: i, Embedding: vec})
	}
	Debug("[peer] %q embedded %d input(s) in %s", k.Label, len(inputs), time.Since(started).Round(time.Millisecond))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
	touchPeerKey(k)
}
